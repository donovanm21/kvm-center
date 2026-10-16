package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileEntry struct {
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type BrowseResponse struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// defaultBrowseRoots is the always-allowed base set. Even on a host
// whose pools haven't been enumerated yet (or where virsh fails) the
// browser still has a safe place to land instead of falling back to
// the filesystem root. These are browsing roots only; appearing under
// one does not establish ownership or authorize VM disk deletion.
var defaultBrowseRoots = []string{
	"/var/lib/libvirt/images",
	"/var/lib/libvirt/boot",
}

// storageBrowseAllowedRoots returns the directories the storage browser
// is confined to: the active libvirt pool target paths (so the jail
// tracks pools as they're created/destroyed) unioned with
// defaultBrowseRoots. All entries are cleaned absolute paths; relative
// or empty pool paths are dropped. The first element is always a
// default root, so it doubles as the fallback when no path is supplied.
func storageBrowseAllowedRoots() []string {
	roots := make([]string, 0, len(defaultBrowseRoots)+4)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		c := filepath.Clean(p)
		if seen[c] {
			return
		}
		seen[c] = true
		roots = append(roots, c)
	}
	for _, d := range defaultBrowseRoots {
		add(d)
	}
	for _, p := range listStoragePools() {
		add(p.Path)
	}
	return roots
}

// pathWithinAllowedRoots reports whether path is equal to, or nested
// under, one of roots. The comparison is separator-aware so a sibling
// whose name merely prefix-matches a root (e.g.
// `/var/lib/libvirt/images-evil` vs `/var/lib/libvirt/images`) is NOT
// accepted. The path is filepath.Clean'd first so `..` segments are
// collapsed before the check, defeating traversal that would otherwise
// climb above a root. When the path exists on disk its symlinks are
// resolved too, so a symlink planted inside a root cannot point the
// browser back out of the jail. An empty root set rejects everything
// (fail closed).
func pathWithinAllowedRoots(path string, roots []string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	// Resolve symlinks when the target exists; ignore the error for
	// not-yet-existing paths and fall back to the cleaned form.
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	for _, root := range roots {
		root = filepath.Clean(root)
		if clean == root {
			return true
		}
		// Separator-aware containment: root + "/" must prefix clean.
		if strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// handleStorageBrowse lists files in a directory on the hypervisor.
// GET /storage-browse?path=/var/lib/libvirt/images
//
// The path is jailed to the active storage-pool target paths plus a
// default base set (see storageBrowseAllowedRoots). Anything outside
// the jail is rejected with 403 so a caller who reaches the agent
// cannot enumerate the whole filesystem.
func handleStorageBrowse(w http.ResponseWriter, r *http.Request) {
	roots := storageBrowseAllowedRoots()

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		// No path supplied: land on the first allowed root.
		if len(roots) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "no storage roots available"})
			return
		}
		dirPath = roots[0]
	}
	dirPath = filepath.Clean(dirPath)

	if !pathWithinAllowedRoots(dirPath, roots) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "path outside allowed storage roots: " + dirPath,
		})
		return
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "cannot read directory: " + err.Error()})
		return
	}

	// Initialise as empty (not nil) so JSON serialises to `[]` for an
	// empty directory rather than `null`. Pre-fix the SPA broke
	// rendering on `null` entries because the FileBrowserModal's
	// `browse.data && browse.data.entries.length` truthiness check
	// passed (response object was truthy) and then dereferenced
	// `.length` of `null`.
	files := []FileEntry{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileEntry{
			Name:     e.Name(),
			IsDir:    e.IsDir(),
			Size:     info.Size(),
			Modified: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(BrowseResponse{Path: dirPath, Entries: files})
}

// handleStorageUpload receives a multipart file upload and writes it to the target directory.
// POST /storage-upload?path=/var/lib/libvirt/images
func handleStorageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	dirPath := filepath.Clean(r.URL.Query().Get("path"))
	if dirPath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// 10 GB max upload.
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "parse form: " + err.Error()})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "file field required"})
		return
	}
	defer file.Close()

	destName := filepath.Base(header.Filename)
	destPath := filepath.Join(dirPath, destName)

	// Prevent path traversal.
	if !strings.HasPrefix(destPath, dirPath) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid filename"})
		return
	}

	dst, err := os.Create(destPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "create file: " + err.Error()})
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		os.Remove(destPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "write failed: " + err.Error()})
		return
	}

	log.Printf("storage-upload: %s (%d bytes) → %s", destName, written, destPath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"name":    destName,
		"path":    destPath,
		"size":    written,
	})
}

// handleStorageDelete removes a file from the hypervisor.
// DELETE /storage-file?path=/var/lib/libvirt/images/old.iso
func handleStorageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}

	filePath := filepath.Clean(r.URL.Query().Get("path"))
	if filePath == "" || filePath == "/" || filePath == "." {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "cannot delete directories"})
		return
	}

	if err := os.Remove(filePath); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	log.Printf("storage-delete: %s (%.1f MB)", filePath, float64(info.Size())/(1024*1024))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    filePath,
		"message": fmt.Sprintf("Deleted %s (%.1f MB)", filepath.Base(filePath), float64(info.Size())/(1024*1024)),
	})
}
