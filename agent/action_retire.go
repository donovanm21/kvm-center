package main

// RETIRE action (v5.54.0).
//
// Fired by the kvm-center server when an operator clicks Delete in
// Settings → Hypervisors. The agent acknowledges the command (so the
// server's HTTP call returns cleanly), then in a background goroutine:
//
//   1. stops libvirtd-tls.socket — closes the qemu+tls migration
//      listener so peers stop trying to migrate to / from this host;
//   2. removes the boot-order systemd drop-in we installed during
//      enrolment (libvirtd-tls.socket.d/0-bootorder.conf);
//   3. removes the migration-grant qemu hook;
//   4. wipes the cert + key + CA bundle under /etc/kvm-center/tls;
//   5. (optionally) removes /opt/kvm-center binaries;
//   6. disables + stops kvm-center-agent.service — which kills the
//      process running this code.
//
// Order matters: step 6 kills the agent, so it must be last AND
// must happen after the HTTP response has flushed. The handler
// responds 200 immediately and schedules the cleanup ~2s later so
// the server sees a clean reply, not a connection-reset midway.
//
// remove_files=false is honoured for operators who want to keep the
// binary + certs around for diagnostics — the agent stops but the
// files stay. Default is full uninstall to match the operator's
// "delete the host" intent.

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// handleRetire acks the request, then schedules the cleanup goroutine.
func handleRetire(w http.ResponseWriter, req models.CommandRequest) {
	// Body schema parallels what the server sends in host_retire.go:
	//   { "action":"RETIRE", "params":{"remove_files":"true"|"false"} }
	// Default to full uninstall on missing field.
	removeFiles := true
	if v, ok := req.Params["remove_files"]; ok {
		removeFiles = v != "false" && v != "0"
	}

	// Ack first so the server's HTTP call gets a clean 200 before we
	// stop ourselves. Without this, the server sees an EOF mid-read
	// and can't distinguish "agent retired ok" from "agent crashed".
	respondWithFlagsAndFields(w, true, "retire scheduled", false, map[string]any{
		"remove_files": removeFiles,
	})
	log.Printf("retire: scheduled (remove_files=%v) — agent will stop in ~2s", removeFiles)

	// Run cleanup off the request goroutine so the response flushes.
	go runRetireCleanup(removeFiles)
}

// runRetireCleanup performs the actual teardown. Logs each step and
// keeps going on best-effort even if a single step errors — the
// operator can finish manually if anything sticks.
func runRetireCleanup(removeFiles bool) {
	// Brief pause so the HTTP response actually lands.
	time.Sleep(2 * time.Second)

	steps := []struct {
		desc string
		fn   func() error
	}{
		{"stop libvirtd-tls.socket", func() error {
			return runOK("systemctl", "stop", "libvirtd-tls.socket")
		}},
		{"remove libvirtd-tls boot-order drop-in", func() error {
			return removeIfExists("/etc/systemd/system/libvirtd-tls.socket.d/0-bootorder.conf")
		}},
		{"remove migration-grant qemu hook", func() error {
			return removeIfExists("/etc/libvirt/hooks/qemu")
		}},
		{"daemon-reload after drop-in removal", func() error {
			return runOK("systemctl", "daemon-reload")
		}},
	}
	if removeFiles {
		steps = append(steps,
			struct {
				desc string
				fn   func() error
			}{"wipe /etc/kvm-center", func() error {
				return os.RemoveAll("/etc/kvm-center")
			}},
			struct {
				desc string
				fn   func() error
			}{"wipe /opt/kvm-center binaries", func() error {
				// Leave /opt/kvm-center if it's the cwd of this process
				// (binary will be removed once the service unit dies).
				return os.RemoveAll("/opt/kvm-center")
			}},
		)
	}
	// disable + stop come last because stopping kills this process.
	steps = append(steps,
		struct {
			desc string
			fn   func() error
		}{"disable kvm-center-agent.service", func() error {
			return runOK("systemctl", "disable", "kvm-center-agent.service")
		}},
		struct {
			desc string
			fn   func() error
		}{"stop kvm-center-agent.service", func() error {
			// systemctl stop blocks until the unit is inactive, which
			// includes signalling our own process. The exec returns
			// only after this process is being killed.
			return runOK("systemctl", "stop", "kvm-center-agent.service")
		}},
	)

	for _, s := range steps {
		if err := s.fn(); err != nil {
			log.Printf("retire: %s: %v (continuing)", s.desc, err)
			continue
		}
		log.Printf("retire: %s ✓", s.desc)
	}
}

func runOK(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return wrapErr(s, err)
		}
	}
	return err
}

func removeIfExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(path)
}

type wrappedErr struct {
	msg string
	err error
}

func (w wrappedErr) Error() string { return w.msg }
func (w wrappedErr) Unwrap() error { return w.err }

func wrapErr(msg string, err error) error {
	if msg == "" {
		return err
	}
	return wrappedErr{msg: msg, err: err}
}
