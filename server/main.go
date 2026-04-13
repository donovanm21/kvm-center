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

// Version is injected at build time via -ldflags "-X main.Version=v1.0.0".
// Defaults to "dev" for local go build.
var Version = "dev"

func main() {
	addr := flag.String("addr", envOrDefault("KVM_CENTER_ADDR", ":8090"), "listen address")
	dataFile := flag.String("data", envOrDefault("KVM_CENTER_DATA", "/var/lib/kvm-center/data.json"), "JSON persistence file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("kvm-center-server %s", Version)
		os.Exit(0)
	}
	log.Printf("kvm-center-server starting, version=%s", Version)

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
	mux.Handle("/api/vms/notes", requirePerm(store, "vm.edit", http.HandlerFunc(handleVMNotes(store))))
	mux.HandleFunc("/api/storage", handleGetStorage(store))
	mux.Handle("/api/storage/browse", requirePerm(store, "storage.view", http.HandlerFunc(handleStorageBrowse(store))))
	mux.Handle("/api/storage/upload", requirePerm(store, "storage.upload", http.HandlerFunc(handleStorageUpload(store))))
	mux.Handle("/api/storage/file",   requirePerm(store, "storage.delete", http.HandlerFunc(handleStorageDelete(store))))
	mux.Handle("/api/vm-create-info", requirePerm(store, "vm.create", http.HandlerFunc(handleVMCreateInfo(store))))
	mux.Handle("/api/vm-create",      requirePerm(store, "vm.create", http.HandlerFunc(handleVMCreate(store))))
	mux.HandleFunc("/api/command", handleCommand(store))
	mux.HandleFunc("/api/guest-telemetry", handleGuestTelemetry(store))
	mux.HandleFunc("/api/migrations", handleGetMigrations(store))
	mux.HandleFunc("/api/migrations/record", handlePostMigration(store))
	mux.HandleFunc("/api/migrate-status", handleMigrateStatus(store))
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"` + Version + `"}`))
	})

	// ── Auth + RBAC ────────────────────────────────────────────
	mux.HandleFunc("/api/auth/status",   handleAuthStatus(store))
	mux.HandleFunc("/api/auth/register", handleAuthRegister(store))
	mux.HandleFunc("/api/auth/login",    handleAuthLogin(store))
	mux.HandleFunc("/api/auth/logout",   handleAuthLogout(store))
	mux.HandleFunc("/api/auth/me",       handleAuthMe(store))
	mux.HandleFunc("/api/auth/2fa/setup",   handleTOTPSetup(store))
	mux.HandleFunc("/api/auth/2fa/enable",  handleTOTPEnable(store))
	mux.HandleFunc("/api/auth/2fa/disable", handleTOTPDisable(store))
	// Federated sign-in (public): list providers + OAuth2/OIDC start+callback.
	mux.HandleFunc("/api/auth/providers",      handleListProviders(store))
	mux.HandleFunc("/api/auth/oidc/start",     handleOIDCStart(store))
	mux.HandleFunc("/api/auth/oidc/callback",  handleOIDCCallback(store))
	mux.HandleFunc("/api/auth/saml/start",     handleSAMLStart(store))
	mux.HandleFunc("/api/auth/saml/acs",       handleSAMLACS(store))
	mux.HandleFunc("/api/auth/saml/metadata",  handleSAMLMetadata(store))
	// Admin IdP CRUD (requires user.manage).
	mux.Handle("/api/idps",     requirePerm(store, "user.manage", handleIDPsList(store)))
	mux.Handle("/api/idps/new", requirePerm(store, "user.manage", handleIDPsCreate(store)))
	mux.Handle("/api/idps/",    requirePerm(store, "user.manage", handleIDPUpdate(store)))
	// Global feature flags. GET is readable by any signed-in user (the UI
	// needs it to grey out disabled buttons); writes require user.manage.
	mux.HandleFunc("/api/settings",       handleFeaturesGet(store))
	mux.Handle("/api/settings/update",    requirePerm(store, "user.manage", handleFeaturesUpdate(store)))
	// Admin-only user/group/role CRUD (require user.manage permission).
	mux.Handle("/api/users",       requirePerm(store, "user.manage", handleUsersList(store)))
	mux.Handle("/api/users/new",   requirePerm(store, "user.manage", handleUsersCreate(store)))
	mux.Handle("/api/users/",      requirePerm(store, "user.manage", handleUserUpdate(store)))
	mux.Handle("/api/groups",      requirePerm(store, "user.manage", handleGroupsList(store)))
	mux.Handle("/api/groups/new",  requirePerm(store, "user.manage", handleGroupsCreate(store)))
	mux.Handle("/api/groups/",     requirePerm(store, "user.manage", handleGroupUpdate(store)))
	mux.Handle("/api/roles",       requirePerm(store, "user.manage", handleRolesList(store)))
	mux.Handle("/api/roles/new",   requirePerm(store, "user.manage", handleRolesCreate(store)))
	mux.Handle("/api/roles/",      requirePerm(store, "user.manage", handleRoleUpdate(store)))

	// Purge expired sessions every 10 minutes.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			store.PurgeExpiredSessions()
		}
	}()

	// Serve embedded web UI for everything else.
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("server: static embed error: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	handler := corsMiddleware(loggingMiddleware(authMiddleware(store, mux)))

	log.Printf("kvm-center server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
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
