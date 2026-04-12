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

// handleStorageBrowse lists files in a directory on the hypervisor.
// GET /storage-browse?path=/var/lib/libvirt/images
func handleStorageBrowse(w http.ResponseWriter, r *http.Request) {
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/var/lib/libvirt/images"
	}
	dirPath = filepath.Clean(dirPath)

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "cannot read directory: " + err.Error()})
		return
	}

	var files []FileEntry
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
