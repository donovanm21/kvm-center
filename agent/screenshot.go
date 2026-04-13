package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// handleVMScreenshot returns a PNG snapshot of the VM's current console
// framebuffer (GET /screenshot/<vmname>). Calls `virsh screenshot`, which
// writes a PPM by default; we convert to PNG in-process so the browser
// can render it without any host-side ImageMagick / ffmpeg dependency.
//
// Cached for screenshotTTL so the dashboard polling loop doesn't hammer
// libvirt — every ten-second refresh per visible VM is plenty, and
// grabbing a fresh framebuffer costs a QMP round-trip on the host.

const screenshotTTL = 8 * time.Second

type screenshotEntry struct {
	png     []byte
	takenAt time.Time
	err     error // cached negative result (VM stopped, no display, etc.)
}

var (
	screenshotMu    sync.Mutex
	screenshotCache = map[string]*screenshotEntry{}
)

func handleVMScreenshot(w http.ResponseWriter, r *http.Request) {
	vmName := strings.TrimPrefix(r.URL.Path, "/screenshot/")
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || strings.ContainsAny(vmName, "/\\..") {
		http.Error(w, "invalid vm name", http.StatusBadRequest)
		return
	}
	img, err := getVMScreenshot(vmName)
	if err != nil {
		// 204 rather than 500 — makes the UI's fallback path trivial
		// (no error banner, just the icon placeholder).
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache, max-age=0")
	w.Write(img)
}

func getVMScreenshot(vmName string) ([]byte, error) {
	screenshotMu.Lock()
	e, ok := screenshotCache[vmName]
	if ok && time.Since(e.takenAt) < screenshotTTL {
		screenshotMu.Unlock()
		if e.err != nil {
			return nil, e.err
		}
		return e.png, nil
	}
	screenshotMu.Unlock()

	png, err := captureVMScreenshot(vmName)

	screenshotMu.Lock()
	screenshotCache[vmName] = &screenshotEntry{png: png, takenAt: time.Now(), err: err}
	screenshotMu.Unlock()

	return png, err
}

func captureVMScreenshot(vmName string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "kvm-screenshot-*.img")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	cmd := exec.Command("virsh", "screenshot", vmName, "--file", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("virsh screenshot failed: %s", strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 2 {
		return nil, errors.New("empty screenshot")
	}
	// Already PNG? (newer libvirt + qemu produce PNG directly.)
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		return data, nil
	}
	// PPM "P6" binary. Convert in-process.
	if bytes.HasPrefix(data, []byte("P6")) {
		return ppmToPNG(data)
	}
	// Unknown format — log once and give up.
	n := 8
	if len(data) < n {
		n = len(data)
	}
	log.Printf("screenshot: unknown format from virsh (header %x…) for VM %q", data[:n], vmName)
	_ = filepath.Base(path) // keep filepath import live for future expansion
	return nil, errors.New("unsupported screenshot format")
}

// ppmToPNG converts a binary P6 PPM to PNG. P6 layout is:
//   "P6\n<width> <height>\n<maxval>\n<raw RGB bytes>"
// with any amount of whitespace (including comments starting with #)
// between the header tokens.
func ppmToPNG(data []byte) ([]byte, error) {
	r := bytes.NewReader(data)
	magic, err := readToken(r)
	if err != nil || magic != "P6" {
		return nil, errors.New("not a P6 PPM")
	}
	wStr, err := readToken(r)
	if err != nil {
		return nil, err
	}
	hStr, err := readToken(r)
	if err != nil {
		return nil, err
	}
	mStr, err := readToken(r)
	if err != nil {
		return nil, err
	}
	width, err := strconv.Atoi(wStr)
	if err != nil {
		return nil, err
	}
	height, err := strconv.Atoi(hStr)
	if err != nil {
		return nil, err
	}
	maxVal, err := strconv.Atoi(mStr)
	if err != nil {
		return nil, err
	}
	// Consume the single whitespace byte after maxval.
	if _, err := r.ReadByte(); err != nil {
		return nil, err
	}
	if maxVal > 255 {
		return nil, errors.New("16-bit PPM not supported")
	}
	pixels := make([]byte, width*height*3)
	if _, err := io.ReadFull(r, pixels); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := (y*width + x) * 3
			img.Set(x, y, color.RGBA{pixels[i], pixels[i+1], pixels[i+2], 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func readToken(r *bytes.Reader) (string, error) {
	var sb strings.Builder
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		// Skip leading whitespace / comments.
		if sb.Len() == 0 {
			if b == '#' {
				for {
					cb, err := r.ReadByte()
					if err != nil {
						return "", err
					}
					if cb == '\n' {
						break
					}
				}
				continue
			}
			if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
				continue
			}
		}
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			return sb.String(), nil
		}
		sb.WriteByte(b)
	}
}

