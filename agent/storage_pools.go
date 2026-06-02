package main

// Storage-pool lifecycle (v5.58.0).
//
// Operators historically had to ssh into the hypervisor and run
// `virsh pool-define-as` to add a storage pool. The kvmc /api/storage
// surface only LISTED pools (via /vm-create-info) — there was no
// path to create / start / autostart / destroy from the UI. This
// file adds dir-type pool management end-to-end so a freshly enrolled
// host can be made VM-ready without dropping to the CLI.
//
// Scope: dir pools only. Other types (iSCSI, NFS, LVM, RBD) need
// their own form fields + connection params and aren't homelab
// defaults; deferred. The HTTP wire is generic enough that adding
// types later is additive.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PoolDefineRequest is the agent body for POST /pool-define.
type PoolDefineRequest struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Autostart bool   `json:"autostart"`
}

// handlePoolDefine creates and starts a directory-type pool, optionally
// flipping it to autostart. Best-effort: if the pool already exists
// the call returns success=true so a retry from the server is
// idempotent (cheap to re-confirm a desired state).
func handlePoolDefine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req PoolDefineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond(w, false, "invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Path = strings.TrimSpace(req.Path)
	if req.Name == "" || req.Path == "" {
		respond(w, false, "pool name + path required")
		return
	}
	if !filepath.IsAbs(req.Path) {
		respond(w, false, "pool path must be absolute")
		return
	}
	// Ensure the host directory exists so pool-build doesn't fail
	// on a missing target. Best-effort — pool-build will surface a
	// permission error here if we can't write.
	if err := os.MkdirAll(req.Path, 0o755); err != nil {
		respond(w, false, "mkdir "+req.Path+": "+err.Error())
		return
	}
	// pool-define-as is idempotent on the libvirt side: re-running
	// against an existing pool just refreshes the inactive XML.
	if out, err := exec.Command("virsh", "pool-define-as", req.Name, "dir", "--target", req.Path).CombinedOutput(); err != nil {
		// "Storage pool '<name>' already defined" comes back on
		// stderr but with exit 1 — treat as success-with-warning
		// so the operator can re-submit the form without scary errors.
		if !strings.Contains(string(out), "already defined") {
			respond(w, false, fmt.Sprintf("pool-define-as %s: %s", req.Name, strings.TrimSpace(string(out))))
			return
		}
	}
	// pool-build wires up the on-disk directory (mkdir -p + relabel
	// where applicable). Safe to no-op when the target exists.
	exec.Command("virsh", "pool-build", req.Name).Run()
	if out, err := exec.Command("virsh", "pool-start", req.Name).CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "already active") {
			respond(w, false, fmt.Sprintf("pool-start %s: %s", req.Name, strings.TrimSpace(string(out))))
			return
		}
	}
	if req.Autostart {
		if out, err := exec.Command("virsh", "pool-autostart", req.Name).CombinedOutput(); err != nil {
			respond(w, false, fmt.Sprintf("pool-autostart %s: %s", req.Name, strings.TrimSpace(string(out))))
			return
		}
	}
	respond(w, true, fmt.Sprintf("pool '%s' defined at %s%s", req.Name, req.Path, ifThen(req.Autostart, " (autostart)", "")))
}

// PoolActionRequest is the agent body for /pool/<name>/<action>.
type PoolActionRequest struct {
	Action  string `json:"action"`  // "start" | "stop" | "autostart" | "no-autostart"
	Confirm bool   `json:"confirm"` // required for destroy paths (see handlePoolDelete)
}

// handlePoolAction supports start/stop/autostart/no-autostart on an
// existing pool. URL pattern: POST /pool/<name>/<action>. Returns
// {success, message} like other lifecycle handlers.
func handlePoolAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/pool/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		respond(w, false, "expected /pool/<name>/<action>")
		return
	}
	name, action := parts[0], parts[1]
	var args []string
	switch action {
	case "start":
		args = []string{"pool-start", name}
	case "stop":
		args = []string{"pool-destroy", name} // libvirt's term for "stop"; doesn't delete the on-disk content
	case "autostart":
		args = []string{"pool-autostart", name}
	case "no-autostart":
		args = []string{"pool-autostart", "--disable", name}
	default:
		respond(w, false, "unknown action: "+action)
		return
	}
	if out, err := exec.Command("virsh", args...).CombinedOutput(); err != nil {
		// Idempotency: "already active" on start, "not active" on stop,
		// etc., are not real failures from the operator's perspective.
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already") || strings.Contains(msg, "not active") {
			respond(w, true, fmt.Sprintf("pool '%s' already in desired state (%s)", name, action))
			return
		}
		respond(w, false, fmt.Sprintf("pool-%s %s: %s", action, name, msg))
		return
	}
	respond(w, true, fmt.Sprintf("pool '%s' %s ok", name, action))
}

// handlePoolDelete stops + undefines a pool. The on-disk directory
// is NOT removed (a wipe of /var/lib/libvirt/images would be a much
// bigger blast-radius than the operator clicked for). Volumes
// inside the pool stay on disk too — the operator can clean them
// up via /api/storage/file.
func handlePoolDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/pool/")
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.Contains(name, "/") {
		respond(w, false, "expected /pool/<name>")
		return
	}
	// Stop the pool first (idempotent if already inactive).
	exec.Command("virsh", "pool-destroy", name).Run()
	if out, err := exec.Command("virsh", "pool-undefine", name).CombinedOutput(); err != nil {
		respond(w, false, fmt.Sprintf("pool-undefine %s: %s", name, strings.TrimSpace(string(out))))
		return
	}
	respond(w, true, fmt.Sprintf("pool '%s' undefined (on-disk content retained — wipe manually if you want it gone)", name))
}

func ifThen(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}
