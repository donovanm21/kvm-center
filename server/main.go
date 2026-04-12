package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed static
var staticFiles embed.FS

func main() {
	addr := flag.String("addr", envOrDefault("KVM_CENTER_ADDR", ":8090"), "listen address")
	dataFile := flag.String("data", envOrDefault("KVM_CENTER_DATA", "/var/lib/kvm-center/data.json"), "JSON persistence file")
	flag.Parse()

	// Ensure data directory exists.
	if err := os.MkdirAll(dirOf(*dataFile), 0755); err != nil {
		log.Fatalf("server: cannot create data dir: %v", err)
	}

	store := NewStore(*dataFile)

	// Background: mark offline hosts and persist every 30 seconds.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			store.MarkOfflineHosts()
			store.Save()
		}
	}()

	mux := http.NewServeMux()

	// API routes.
	mux.HandleFunc("/api/telemetry", handleTelemetry(store))
	mux.HandleFunc("/api/hosts", handleGetHosts(store))
	mux.HandleFunc("/api/hosts/", handleGetHost(store))
	mux.HandleFunc("/api/vms", handleGetVMs(store))
	mux.HandleFunc("/api/vms/snapshots", handleVMSnapshots(store))
	mux.HandleFunc("/api/vms/notes", handleVMNotes(store))
	mux.HandleFunc("/api/storage", handleGetStorage(store))
	mux.HandleFunc("/api/command", handleCommand(store))
	mux.HandleFunc("/api/guest-telemetry", handleGuestTelemetry(store))
	mux.HandleFunc("/api/migrate-status", handleMigrateStatus(store))
	mux.HandleFunc("/health", handleHealth)

	// Serve embedded web UI for everything else.
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("server: static embed error: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	handler := corsMiddleware(loggingMiddleware(mux))

	log.Printf("kvm-center server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip logging for UI assets and health checks.
		if r.URL.Path != "/health" && len(r.URL.Path) > 4 && r.URL.Path[:4] == "/api" {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
