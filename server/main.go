package main

import (
	"context"
	"crypto/tls"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// staticV4 embeds the built React SPA (webui-react/dist) produced by the
// "vite build" step in the Docker image. In local dev the directory may
// be empty — the /v4/ mux check below handles that gracefully so "go run
// ./server" still works without a Node build pass.
//
//go:embed all:staticv4
var staticV4Files embed.FS

// Version is read from the committed /VERSION file at compile time via
// go:embed — single source of truth shared by the server binary, the
// build-portal page, and anything else that needs to stamp a release.
// Bump the VERSION file alongside the git tag on each release.
//
//go:embed VERSION
var versionRaw string

// grafanaDashboard is the importable dashboard template shipped to
// operators via /api/grafana/dashboard.json. Embedded so it stays
// pinned to the release — edits travel with the binary.
//
//go:embed grafana-dashboard.json
var grafanaDashboard []byte
var Version = strings.TrimSpace(versionRaw)

func main() {
	mongoURL := flag.String("mongo-url", envOrDefault("KVM_CENTER_MONGO_URL", ""), "MongoDB URI — if set, data is persisted in Mongo instead of the JSON file. Leave blank for the JSON backend.")
	mongoDB := flag.String("mongo-db", envOrDefault("KVM_CENTER_MONGO_DB", "kvm-center"), "MongoDB database name (used when -mongo-url is set)")
	addr := flag.String("addr", envOrDefault("KVM_CENTER_ADDR", ":8090"), "HTTP listen address (plain HTTP, used when TLS is off or as the HTTPS-redirect port)")
	tlsAddr := flag.String("tls-addr", envOrDefault("KVM_CENTER_TLS_ADDR", ":8443"), "HTTPS listen address (used when -cert/-key are set or auto-generated)")
	grpcAddr := flag.String("grpc-addr", envOrDefault("KVM_CENTER_GRPC_ADDR", ""), "Optional dedicated gRPC listen address. Leave empty (default) to multiplex gRPC on the TLS port (:8190) via content-type dispatch; set to e.g. :8095 if the operator wants a separate port for firewall or audit reasons.")
	tlsCert := flag.String("cert", envOrDefault("KVM_CENTER_TLS_CERT", ""), "PEM-encoded TLS certificate (if blank, a self-signed cert is auto-generated)")
	tlsKey := flag.String("key", envOrDefault("KVM_CENTER_TLS_KEY", ""), "PEM-encoded private key for -cert (must be set if -cert is set, and vice-versa)")
	tlsDir := flag.String("tls-dir", envOrDefault("KVM_CENTER_TLS_DIR", "/var/lib/kvm-center/tls"), "directory for the auto-generated self-signed cert when -cert/-key are blank")
	disableTLS := flag.Bool("no-tls", envOrDefault("KVM_CENTER_NO_TLS", "") == "1", "run plain HTTP only (skip cert bootstrap)")
	dataFile := flag.String("data", envOrDefault("KVM_CENTER_DATA", "/var/lib/kvm-center/data.json"), "JSON persistence file")
	caDir := flag.String("ca-dir", envOrDefault("KVM_CENTER_CA_DIR", "/var/lib/kvm-center/ca"), "directory for the cluster CA cert + private key (auto-generated on first boot)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	// --cert and --key must be supplied together; either flag alone is a
	// config error worth surfacing up front rather than serving HTTP by
	// accident.
	if (*tlsCert != "") != (*tlsKey != "") {
		log.Fatalf("server: %v", errTLSPathsIncomplete)
	}

	if *showVersion {
		log.Printf("kvm-center-server %s", Version)
		os.Exit(0)
	}
	log.Printf("kvm-center-server starting, version=%s", Version)

	// Ensure data directory exists.
	if err := os.MkdirAll(dirOf(*dataFile), 0755); err != nil {
		log.Fatalf("server: cannot create data dir: %v", err)
	}

	// Pick the persistence backend. Mongo wins when a URL is provided;
	// otherwise we stay on the original JSON file so an existing v5.0
	// install boots unchanged. First-boot migration from JSON → Mongo
	// happens transparently if a data.json is found alongside an empty
	// Mongo — see bootstrapBackend for the detailed dance.
	backend := bootstrapBackend(*mongoURL, *mongoDB, *dataFile)
	log.Printf("backend: using %s", backend.Name())
	store := NewStoreWithBackend(backend, *dataFile)

	// Kick off the audit-log shipper worker. Safe to call even when
	// no shipping config exists yet — worker no-ops until the operator
	// saves a config via Settings → Audit Log → Shipping.
	// Bootstrap the cluster CA (v5.10.0). Controls the PKI used for
	// hypervisor mTLS + signed migration grants in upcoming patches.
	clusterCA, err := EnsureClusterCA(*caDir)
	if err != nil {
		log.Fatalf("server: cluster CA: %v", err)
	}
	log.Printf("ca: cluster CA loaded from %s (NotAfter=%s)", *caDir, clusterCA.Cert().NotAfter.Format(time.RFC3339))

	// Grant signer (v5.11). Ed25519 key used to sign per-migration
	// authorization tokens. Stored next to the cluster CA so a single
	// volume backs everything the control plane needs to survive a
	// restart.
	grantSigner, err := EnsureGrantSigner(*caDir)
	if err != nil {
		log.Fatalf("server: grant signer: %v", err)
	}
	serverGrantSigner = grantSigner
	log.Printf("grants: Ed25519 signer ready (pub=%s…)", grantSigner.PublicKeyHex()[:16])

	// Metrics sink (v5.13). No-op until the operator enables it via
	// Settings → Metrics; starting the worker early avoids a race
	// where the first telemetry tick arrives before the queue exists.
	store.startMetricsWorker(context.Background())

	store.startAuditShipper(context.Background())
	store.startSnapshotScheduler(context.Background())
	store.startNotificationDispatcher(context.Background())
	hub := NewSSEHub()
	store.hub = hub // state-changing store methods publish via this handle

	// TLS bootstrap: operator-supplied cert takes priority; otherwise
	// generate a self-signed pair into -tls-dir so HTTPS works out of
	// the box on a fresh install. -no-tls skips the whole dance for
	// plain-HTTP dev / tests.
	certPath, keyPath := *tlsCert, *tlsKey
	var reloader *CertReloader
	if !*disableTLS {
		if certPath == "" && keyPath == "" {
			certPath = *tlsDir + "/server.crt"
			keyPath = *tlsDir + "/server.key"
			if err := EnsureSelfSigned(certPath, keyPath); err != nil {
				log.Fatalf("server: tls bootstrap: %v", err)
			}
			log.Printf("tls: using auto-generated self-signed cert at %s", certPath)
		} else {
			log.Printf("tls: using operator-supplied cert at %s", certPath)
		}
		reloader = NewCertReloader(certPath, keyPath)
		// Prime the cache + surface any load error at startup so we
		// don't defer discovery to the first client handshake.
		if _, err := reloader.GetCertificate(nil); err != nil {
			log.Fatalf("server: tls cert load: %v", err)
		}
	}

	// Background: expiry watcher. Covers three certificate classes:
	//   - server TLS leaf (self-signed by default or operator-supplied)
	//   - cluster CA (ECDSA P-256, 10-year default; signs hypervisor certs)
	//   - each enrolled hypervisor's leaf cert (used for mTLS migration)
	// Any class going silently past its expiry breaks migrations with
	// no UI signal (reported as Tier S #8, roadmapped 2026-04-20).
	// Fires one warning per (kind, identity) transition into the
	// threshold window so operators don't get spammed every tick.
	go startExpiryWatcher(store, reloader, clusterCA)

	// Background: mark offline hosts and persist every 30 seconds.
	// Phantom-host prune runs on a separate, slower tick (hourly) so
	// it can't thrash the store under load and so a noisy audit log
	// doesn't complicate a storm of MarkOfflineHosts calls.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			store.MarkOfflineHosts()
			store.Save()
		}
	}()
	go func() {
		// Stagger: first prune after 10 min, then hourly. No need to
		// race with the post-boot reconcile storm.
		time.Sleep(10 * time.Minute)
		store.PrunePhantomHosts()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			store.PrunePhantomHosts()
		}
	}()

	mux := http.NewServeMux()

	// API routes.
	mux.HandleFunc("/api/events", handleSSEEvents(hub))
	mux.HandleFunc("/api/telemetry", handleTelemetry(store))
	mux.HandleFunc("/api/hosts", handleGetHosts(store))
	// Maintenance routes share the /api/hosts/ prefix; dispatch by suffix.
	mux.HandleFunc("/api/hosts/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/maintenance/enter"):
			requirePerm(store, "host.manage", http.HandlerFunc(handleMaintenanceEnter(store))).ServeHTTP(w, r)
		case strings.HasSuffix(r.URL.Path, "/maintenance/exit"):
			requirePerm(store, "host.manage", http.HandlerFunc(handleMaintenanceExit(store))).ServeHTTP(w, r)
		case strings.HasSuffix(r.URL.Path, "/metrics"):
			handleHostMetrics(store).ServeHTTP(w, r)
		case r.Method == http.MethodDelete:
			requirePerm(store, "host.manage", http.HandlerFunc(handleDeleteHost(store))).ServeHTTP(w, r)
		case strings.HasSuffix(r.URL.Path, "/update-agent"):
			requirePerm(store, "host.manage", http.HandlerFunc(handleUpdateAgent(store))).ServeHTTP(w, r)
		default:
			handleGetHost(store).ServeHTTP(w, r)
		}
	})
	mux.Handle("/api/audit", requirePerm(store, "audit.view", http.HandlerFunc(handleAuditList(store))))
	mux.Handle("/api/snapshot-policies", requirePerm(store, "vm.snapshot", http.HandlerFunc(handleSnapshotPolicies(store))))
	mux.Handle("/api/snapshot-policies/", requirePerm(store, "vm.snapshot", http.HandlerFunc(handleSnapshotPolicy(store))))
	mux.Handle("/api/settings/audit-shipping", requirePerm(store, "user.manage", http.HandlerFunc(auditShippingRouter(store))))
	mux.Handle("/api/settings/audit-shipping/test", requirePerm(store, "user.manage", http.HandlerFunc(handleAuditShippingTest(store))))
	mux.Handle("/api/settings/notifications", requirePerm(store, "user.manage", http.HandlerFunc(handleNotifications(store))))
	mux.Handle("/api/settings/notifications/test", requirePerm(store, "user.manage", http.HandlerFunc(handleNotificationTest(store))))
	mux.Handle("/api/settings/metrics", requirePerm(store, "user.manage", http.HandlerFunc(handleMetricsSettings(store))))
	mux.Handle("/api/settings/metrics/test", requirePerm(store, "user.manage", http.HandlerFunc(handleMetricsTest(store))))
	// Docs (v5.14). Public — reference material, not operational state.
	mux.HandleFunc("/api/docs/tree", handleDocsTree)
	mux.HandleFunc("/api/docs/page/", handleDocsPage)
	mux.HandleFunc("/api/grafana/dashboard.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="kvm-center-dashboard.json"`)
		w.Write(grafanaDashboard)
	})
	// Admin-scoped system tokens (user.manage).
	mux.Handle("/api/tokens", requirePerm(store, "user.manage", http.HandlerFunc(handleAPITokens(store))))
	// Self-service user tokens (any signed-in user). More-specific path
	// must be registered before /api/tokens/ so http.ServeMux picks the
	// right handler.
	mux.Handle("/api/tokens/me", http.HandlerFunc(handleMyAPITokens(store)))
	mux.Handle("/api/tokens/me/", http.HandlerFunc(handleMyAPIToken(store)))
	mux.Handle("/api/tokens/", requirePerm(store, "user.manage", http.HandlerFunc(handleAPIToken(store))))

	// Command log (v5.32.17). Admin-only: shows the last N /api/command
	// invocations with actor + outcome, and surfaces any "unknown"
	// entries the boot reconciler flagged.
	mux.Handle("/api/admin/command-log", requirePerm(store, "user.manage", http.HandlerFunc(handleCommandLog(store))))

	// Hypervisor PKI (v5.10.0). Admin-gated except /sign (which is
	// protected by the one-time enrollment token) and /bundle (public
	// trust anchor).
	mux.Handle("/api/ca/enroll", requirePerm(store, "tls.manage", http.HandlerFunc(handleCAEnroll(store))))
	mux.HandleFunc("/api/ca/sign", handleCASign(store, clusterCA))
	mux.HandleFunc("/api/ca/bundle", handleCABundle(clusterCA))
	// Grant pub-key (v5.11). Public trust material, so served without
	// auth so fresh agents can fetch during first contact.
	mux.HandleFunc("/api/migrations/grant-pubkey", handleGrantPubKey(grantSigner))
	mux.Handle("/api/ca/hypervisors", requirePerm(store, "tls.manage", http.HandlerFunc(handleCAHypervisors(store, clusterCA))))
	mux.Handle("/api/ca/hypervisors/", requirePerm(store, "tls.manage", http.HandlerFunc(handleCARevoke(store))))
	mux.HandleFunc("/api/vms", handleGetVMs(store))
	mux.HandleFunc("/api/vms/snapshots", handleVMSnapshots(store))
	mux.HandleFunc("/api/vms/screenshot", handleVMScreenshot(store))
	mux.Handle("/api/vms/notes", requirePerm(store, "vm.edit", http.HandlerFunc(handleVMNotes(store))))
	mux.Handle("/api/vms/console-token", requirePerm(store, "vm.console", http.HandlerFunc(handleConsoleToken(store))))
	// Server-proxied console (v5.0.4+): noVNC HTML and the VNC WebSocket
	// both ride through the server's HTTPS origin so every hit is
	// re-validated against the operator's session cookie. Sharing the
	// URL with someone who doesn't carry that cookie does nothing.
	mux.Handle("/api/console/", requirePerm(store, "vm.console", http.HandlerFunc(handleConsolePage(store))))
	mux.Handle("/api/console-ws/", requirePerm(store, "vm.console", http.HandlerFunc(handleConsoleWSProxy(store))))
	mux.HandleFunc("/api/storage", handleGetStorage(store))
	mux.Handle("/api/storage/browse", requirePerm(store, "storage.view", http.HandlerFunc(handleStorageBrowse(store))))
	mux.Handle("/api/storage/upload", requirePerm(store, "storage.upload", http.HandlerFunc(handleStorageUpload(store))))
	mux.Handle("/api/storage/file", requirePerm(store, "storage.delete", http.HandlerFunc(handleStorageDelete(store))))
	mux.Handle("/api/vm-create-info", requirePerm(store, "vm.create", http.HandlerFunc(handleVMCreateInfo(store))))
	mux.Handle("/api/vm-create", requirePerm(store, "vm.create", http.HandlerFunc(handleVMCreate(store))))
	mux.HandleFunc("/api/command", handleCommand(store))
	mux.HandleFunc("/api/guest-telemetry", handleGuestTelemetry(store))
	mux.HandleFunc("/api/migrations", handleGetMigrations(store))
	mux.HandleFunc("/api/migrations/record", handlePostMigration(store))
	mux.Handle("/api/migrations/cancel", requirePerm(store, "vm.migrate", http.HandlerFunc(handleCancelMigration(store))))
	// Force-fail (v5.32.18): admin-only escape hatch when the normal
	// cancel path refuses. Does NOT talk to the agent — operator has
	// presumably resolved the VM state manually.
	mux.Handle("/api/migrations/force-fail", requirePerm(store, "user.manage", http.HandlerFunc(handleForceFailMigration(store))))
	mux.HandleFunc("/api/migrate-status", handleMigrateStatus(store))
	mux.HandleFunc("/api/kvm-tools/version", handleKVMToolsVersion)
	mux.Handle("/api/kvm-tools/update", requirePerm(store, "vm.edit", http.HandlerFunc(handleKVMToolsUpdate(store))))

	// TLS management — status is readable by any signed-in user so the
	// UI can show the expiry badge; mutations require tls.manage.
	if reloader != nil {
		mux.HandleFunc("/api/tls/status", handleTLSStatus(reloader, certPath, keyPath))
		mux.HandleFunc("/api/tls/cert.pem", handleTLSCertDownload(certPath))
		mux.Handle("/api/tls/upload", requirePerm(store, "tls.manage", auditTLSHandler(store, AuditTLSUpload, handleTLSUpload(certPath, keyPath))))
		mux.Handle("/api/tls/regenerate", requirePerm(store, "tls.manage", auditTLSHandler(store, AuditTLSRegenerate, handleTLSRegenerate(certPath, keyPath))))
	}
	mux.HandleFunc("/health", handleHealth)

	// /downloads/* serves the cross-compiled agent / kvm-tools / kvmctl
	// binaries plus install scripts. v5.16 folded this in from the
	// standalone kvm-center-build container (port 8097). Unauthenticated
	// by design — install scripts need to be pullable before enrollment,
	// and the enrollment token is what actually gates cluster access.
	if dir := os.Getenv("KVM_CENTER_DOWNLOADS_DIR"); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			mux.Handle("/downloads/", http.StripPrefix("/downloads/", http.FileServer(http.Dir(dir))))
			log.Printf("downloads: serving %s under /downloads/", dir)
		} else {
			log.Printf("downloads: KVM_CENTER_DOWNLOADS_DIR=%s is not a directory; /downloads/ disabled", dir)
		}
	}
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"` + Version + `"}`))
	})

	// ── Auth + RBAC ────────────────────────────────────────────
	mux.HandleFunc("/api/auth/status", handleAuthStatus(store))
	mux.HandleFunc("/api/auth/register", handleAuthRegister(store))
	mux.HandleFunc("/api/auth/login", handleAuthLogin(store))
	mux.HandleFunc("/api/auth/logout", handleAuthLogout(store))
	mux.HandleFunc("/api/auth/me", handleAuthMe(store))
	mux.HandleFunc("/api/auth/2fa/setup", handleTOTPSetup(store))
	mux.HandleFunc("/api/auth/2fa/enable", handleTOTPEnable(store))
	mux.HandleFunc("/api/auth/2fa/disable", handleTOTPDisable(store))
	// Federated sign-in (public): list providers + OAuth2/OIDC start+callback.
	mux.HandleFunc("/api/auth/providers", handleListProviders(store))
	mux.HandleFunc("/api/auth/oidc/start", handleOIDCStart(store))
	mux.HandleFunc("/api/auth/oidc/callback", handleOIDCCallback(store))
	mux.HandleFunc("/api/auth/saml/start", handleSAMLStart(store))
	mux.HandleFunc("/api/auth/saml/acs", handleSAMLACS(store))
	mux.HandleFunc("/api/auth/saml/metadata", handleSAMLMetadata(store))
	// Admin IdP CRUD (requires user.manage).
	mux.Handle("/api/idps", requirePerm(store, "user.manage", handleIDPsList(store)))
	mux.Handle("/api/idps/new", requirePerm(store, "user.manage", handleIDPsCreate(store)))
	mux.Handle("/api/idps/", requirePerm(store, "user.manage", handleIDPUpdate(store)))
	// Global feature flags. GET is readable by any signed-in user (the UI
	// needs it to grey out disabled buttons); writes require user.manage.
	mux.HandleFunc("/api/settings", handleFeaturesGet(store))
	mux.Handle("/api/settings/update", requirePerm(store, "user.manage", handleFeaturesUpdate(store)))

	// First-boot setup wizard (v5.20). /state is public because the SPA
	// needs to read it before the user signs in; /complete requires
	// user.manage so only admins can close out the wizard.
	backfillSetupComplete(store)
	// Clean up migration records stranded by a server restart (bug:
	// live migrate HTTP response goes to a closed socket across a
	// restart, so finalizeMigration never runs).
	go reconcileStrandedMigrations(store)
	// Command log: flip any "pending" entries older than the cutoff
	// to "unknown" so the admin UI surfaces them. We can't tell
	// whether the agent actually ran them — hence "unknown", not
	// "failed". Tier S #4, v5.32.17.
	go reconcileStrandedCommands(store)
	seedPublicURLFromEnv(store)
	notifyStore = store
	mux.HandleFunc("/api/setup/state", handleSetupState(store))
	mux.Handle("/api/settings/public-url", requirePerm(store, "user.manage", handlePublicURL(store)))
	mux.Handle("/api/setup/complete", requirePerm(store, "user.manage", http.HandlerFunc(handleSetupComplete(store))))
	// Backup (v5.27): admin-only snapshot export. Restore lives in
	// scripts/bootstrap.sh --restore because replacing live state
	// in-process is hazardous.
	mux.Handle("/api/admin/backup", requirePerm(store, "user.manage", handleBackup(store)))
	// Database backend status + migration (v5.1+). Gated on user.manage;
	// considered a platform-ops operation alongside TLS management.
	mux.Handle("/api/settings/database", requirePerm(store, "user.manage", handleDatabaseStatus(store)))
	mux.Handle("/api/settings/database/test", requirePerm(store, "user.manage", handleDatabaseTest()))
	mux.Handle("/api/settings/database/migrate", requirePerm(store, "user.manage", handleDatabaseMigrate(store)))
	// Admin-only user/group/role CRUD (require user.manage permission).
	mux.Handle("/api/users", requirePerm(store, "user.manage", handleUsersList(store)))
	mux.Handle("/api/users/new", requirePerm(store, "user.manage", handleUsersCreate(store)))
	mux.Handle("/api/users/", requirePerm(store, "user.manage", handleUserUpdate(store)))
	mux.Handle("/api/groups", requirePerm(store, "user.manage", handleGroupsList(store)))
	mux.Handle("/api/groups/new", requirePerm(store, "user.manage", handleGroupsCreate(store)))
	mux.Handle("/api/groups/", requirePerm(store, "user.manage", handleGroupUpdate(store)))
	mux.Handle("/api/roles", requirePerm(store, "user.manage", handleRolesList(store)))
	mux.Handle("/api/roles/new", requirePerm(store, "user.manage", handleRolesCreate(store)))
	mux.Handle("/api/roles/", requirePerm(store, "user.manage", handleRoleUpdate(store)))

	// Purge expired sessions every 10 minutes.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			store.PurgeExpiredSessions()
		}
	}()

	// v5.18.9: legacy v3 HTML UI retired. The React SPA is the only
	// shipped UI now; /legacy and /v4 bookmarks 308 to the SPA root.
	sub4, err := fs.Sub(staticV4Files, "staticv4/dist")
	if err != nil {
		log.Fatalf("server: v4 SPA bundle not embedded: %v", err)
	}
	mux.Handle("/", spaHandler(sub4))
	mux.Handle("/legacy", http.RedirectHandler("/", http.StatusPermanentRedirect))
	mux.Handle("/legacy/", http.RedirectHandler("/", http.StatusPermanentRedirect))
	mux.Handle("/v4", http.RedirectHandler("/", http.StatusPermanentRedirect))
	mux.Handle("/v4/", http.RedirectHandler("/", http.StatusPermanentRedirect))

	handler := corsMiddleware(loggingMiddleware(authMiddleware(store, mux)))

	// gRPC agent service (v5.27+). The in-process gRPC server rides
	// the same :8190 TLS listener as the HTTP API via content-type
	// dispatch (see tlsServer.Handler below). A standalone :8095
	// listener is still supported for the rare operator who wants a
	// dedicated port + different firewall rules — set
	// KVM_CENTER_GRPC_ADDR=0.0.0.0:8095 to enable. Default is the
	// multiplex (empty → no separate listener).
	grpcSrv := buildGRPCServer(store)
	if *grpcAddr != "" {
		stop, err := startGRPCServerOn(*grpcAddr, grpcSrv)
		if err != nil {
			log.Printf("grpc: dedicated listener disabled (%v)", err)
		} else {
			defer stop()
		}
	}

	if reloader == nil {
		log.Printf("kvm-center server listening on %s (plain HTTP, TLS disabled)", *addr)
		if err := http.ListenAndServe(*addr, handler); err != nil {
			log.Fatalf("server: %v", err)
		}
		return
	}

	// HTTPS on tlsAddr serves the full handler for browser users. Plain
	// HTTP on addr keeps serving /api/ unchanged so hypervisor agents
	// (which don't carry our self-signed CA) can keep posting telemetry
	// without TLS changes on their side; anything that smells like a
	// browser landing page (non-/api/ GET) is 308'd to the HTTPS URL.
	//
	// gRPC multiplex (v5.27+): a request with Content-Type
	// application/grpc (HTTP/2 only) fans out to the in-process
	// gRPC server; everything else goes through the REST handler.
	// Sharing the port means one TLS cert, one firewall rule, one
	// listen address in the agent's config.
	muxedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcSrv.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	tlsServer := &http.Server{
		Addr:    *tlsAddr,
		Handler: muxedHandler,
		TLSConfig: &tls.Config{
			GetCertificate: reloader.GetCertificate,
			MinVersion:     tls.VersionTLS12,
			// HTTP/2 is required for gRPC — advertise h2 in ALPN.
			NextProtos: []string{"h2", "http/1.1"},
		},
	}
	// v5.32.2: plain-HTTP :8090 kept alive for legacy kvm-tools running
	// inside guest VMs. Those were installed pre-v5.32 with
	// http://…:8090 in their config and can't self-rotate without server
	// reachability — a chicken-and-egg we broke in v5.32.1. The HV-side
	// agent wire path has fully migrated to TLS (:8190) regardless.
	// Set KVM_CENTER_DISABLE_HTTP=1 to force TLS-only (useful once the
	// guest fleet is fully rotated).
	if os.Getenv("KVM_CENTER_DISABLE_HTTP") == "1" {
		log.Printf("kvm-center server plain-HTTP listener DISABLED (KVM_CENTER_DISABLE_HTTP=1). Legacy kvm-tools will stop reporting.")
	} else {
		go func() {
			plain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Path
				if strings.HasPrefix(p, "/api/") || p == "/health" || strings.HasPrefix(p, "/downloads/") {
					handler.ServeHTTP(w, r)
					return
				}
				host := r.Host
				if i := strings.Index(host, ":"); i > 0 {
					host = host[:i]
				}
				target := "https://" + host + *tlsAddr + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusPermanentRedirect)
			})
			log.Printf("kvm-center server plain-HTTP on %s (legacy guests; retire via KVM_CENTER_DISABLE_HTTP=1 after fleet rotation)", *addr)
			if err := http.ListenAndServe(*addr, plain); err != nil {
				log.Printf("server: http listener exited: %v", err)
			}
		}()
	}
	log.Printf("kvm-center server listening on %s (TLS)", *tlsAddr)
	if err := tlsServer.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("server: tls: %v", err)
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
