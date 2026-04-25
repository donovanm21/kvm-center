package main

// EDIT_VM action — extracted from the monolithic commander.go switch
// in v5.37.2 (P2 #1, dispatcher refactor). Pure move; no behaviour
// change. The action is the largest single block in the dispatcher
// (~240 lines) and pulling it out lets us:
//   - test the helpers (currentMaxMemKiB, applyXMLEdits, patchDomainXML)
//     without dragging the whole agent's command server into the test
//     harness;
//   - read the dispatcher in one screen (the case statements that
//     remain are ≤ 25 lines each).
//
// Inputs match what the dispatcher already computed: the response
// writer, the parsed CommandRequest, and the resolved vmTarget
// (vm_name preferred over vm_uuid).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"kvm-center/internal/models"
)

// handleEditVM applies hardware edits (vCPUs / memory / autostart /
// XML-only fields) to the named VM. Sends the response on w directly
// — caller does not write further.
func handleEditVM(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
	// Hardware edit. Params (at least one required):
	//   vcpus   = target integer
	//   mem_mb  = target integer MB (balloon target)
	// Flow: check if VM is running. If running, try --live --config.
	// If --live fails OR VM is stopped, fall back to --config.
	// Response carries requires_reboot=true when the change didn't
	// apply live — SPA surfaces a "shut down to apply?" prompt.
	stateOut, _ := exec.Command("virsh", "domstate", vmTarget).Output()
	isRunning := strings.TrimSpace(string(stateOut)) == "running"

	var msgs []string
	requiresReboot := false
	applyEdit := func(kind, val string, virshArgs []string) error {
		liveArgs := append(append([]string{}, virshArgs...), "--config", "--live")
		confArgs := append(append([]string{}, virshArgs...), "--config")
		if isRunning {
			if o, e := exec.Command("virsh", liveArgs...).CombinedOutput(); e == nil {
				msgs = append(msgs, kind+"="+val+" (live + config)")
				return nil
			} else {
				// Live rejected — save to config so it applies on reboot.
				if o2, e2 := exec.Command("virsh", confArgs...).CombinedOutput(); e2 != nil {
					return fmt.Errorf("%s failed: live=%s config=%s",
						kind, strings.TrimSpace(string(o)), strings.TrimSpace(string(o2)))
				}
				msgs = append(msgs, kind+"="+val+" (config saved — reboot required to apply)")
				requiresReboot = true
				return nil
			}
		}
		// VM not running — config-only.
		if o, e := exec.Command("virsh", confArgs...).CombinedOutput(); e != nil {
			return fmt.Errorf("%s failed: %s", kind, strings.TrimSpace(string(o)))
		}
		msgs = append(msgs, kind+"="+val+" (config; applies on next boot)")
		return nil
	}

	if v := strings.TrimSpace(req.Params["vcpus"]); v != "" {
		if err := applyEdit("vcpus", v, []string{"setvcpus", vmTarget, v}); err != nil {
			// v5.27.3: the common libvirt failure here is "requested
			// vcpus is greater than max allowable vcpus". Libvirt's
			// `setvcpus --maximum` can raise the cap, but only when
			// the VM is shut off and only with --config. Translate
			// the error into a clearer message so the SPA can
			// surface a "shut down + increase vCPU max?" prompt.
			if strings.Contains(err.Error(), "greater than max allowable") {
				// v5.31.0: if the operator opted in via
				// allow_shutdown=true, shut the VM down ourselves,
				// raise the cap, apply the count, then power back
				// on. One click from their side.
				if isRunning && req.Params["allow_shutdown"] == "true" {
					log.Printf("edit_vm: allow_shutdown=true — power-cycling %s to raise vCPU cap to %s", vmTarget, v)
					if _, pcErr := powerCycleForEdit(vmTarget); pcErr != nil {
						respond(w, false, "shutdown for vCPU cap raise failed: "+pcErr.Error())
						return
					}
					// VM is now shut off — raise cap + apply count.
					if o, e := exec.Command("virsh", "setvcpus", vmTarget, v, "--maximum", "--config").CombinedOutput(); e != nil {
						// Best-effort: bring the VM back up before reporting.
						// Log the restart outcome so the operator sees if
						// the VM is left shut off — silent-swallow used to
						// produce misleading "setvcpus failed" alerts when
						// the actual operational hit was a failed restart.
						// Tier 2 #6, v5.37.0.
						if so, se := exec.Command("virsh", "start", vmTarget).CombinedOutput(); se != nil {
							log.Printf("edit_vm: %s left SHUT OFF after setvcpus --maximum rollback (start error: %s)", vmTarget, strings.TrimSpace(string(so)))
						}
						respond(w, false, "setvcpus --maximum failed: "+strings.TrimSpace(string(o)))
						return
					}
					if o2, e2 := exec.Command("virsh", "setvcpus", vmTarget, v, "--config").CombinedOutput(); e2 != nil {
						if so, se := exec.Command("virsh", "start", vmTarget).CombinedOutput(); se != nil {
							log.Printf("edit_vm: %s left SHUT OFF after setvcpus rollback (start error: %s)", vmTarget, strings.TrimSpace(string(so)))
						}
						respond(w, false, "setvcpus (after raising max) failed: "+strings.TrimSpace(string(o2)))
						return
					}
					if o3, e3 := exec.Command("virsh", "start", vmTarget).CombinedOutput(); e3 != nil {
						respond(w, false, "VM failed to restart after cap raise: "+strings.TrimSpace(string(o3)))
						return
					}
					msgs = append(msgs, "vcpus="+v+" (cap raised via shutdown+start)")
					isRunning = true // VM is back up; keeps subsequent mem_mb path correct.
				} else if isRunning {
					// HTTP 200 (not 500) so the SPA's mutation sees
					// the body via onSuccess and can show the
					// "Shut down, raise cap, restart" follow-up
					// modal. The business rejection is carried in
					// success:false, not the status code.
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(models.CommandResponse{
						Success: false,
						Message: "cannot raise the VM's vCPU cap while it's running. Shut the VM down first, or re-submit with allow_shutdown=true so the agent does it for you (shutdown → raise cap → apply count → restart).",
					})
					return
				} else {
					// Shut off — try to raise the cap + apply.
					if o, e := exec.Command("virsh", "setvcpus", vmTarget, v, "--maximum", "--config").CombinedOutput(); e != nil {
						respond(w, false, "setvcpus --maximum failed: "+strings.TrimSpace(string(o)))
						return
					}
					if o2, e2 := exec.Command("virsh", "setvcpus", vmTarget, v, "--config").CombinedOutput(); e2 != nil {
						respond(w, false, "setvcpus (after raising max) failed: "+strings.TrimSpace(string(o2)))
						return
					}
					msgs = append(msgs, "vcpus="+v+" (cap raised + config; applies on next boot)")
				}
				// Fall through so any mem_mb / autostart / boot_dev
				// edits in the same request still apply. The
				// running-without-allow_shutdown branch above
				// already returned; everything else either handled
				// the cap raise or was shut off to begin with.
			} else {
				respond(w, false, err.Error())
				return
			}
		}
	}
	// Autostart: works live regardless of VM state — virsh autostart
	// doesn't touch running QEMU; it just updates libvirt's
	// per-domain metadata.
	if as := strings.TrimSpace(req.Params["autostart"]); as != "" {
		args := []string{"autostart", vmTarget}
		if as == "false" || as == "0" || as == "off" {
			args = append(args, "--disable")
		}
		if o, e := exec.Command("virsh", args...).CombinedOutput(); e != nil {
			respond(w, false, "autostart failed: "+strings.TrimSpace(string(o)))
			return
		}
		msgs = append(msgs, "autostart="+as)
	}

	// XML-only fields (cpu_mode, video_model, boot_dev) require the
	// VM to be shut off — they live in the domain XML and libvirt
	// will block a live reload. Collect them here; apply after the
	// standalone vcpus/mem_mb steps so error ordering is sensible.
	xmlEdits := map[string]string{}
	for _, key := range []string{"cpu_mode", "video_model", "boot_dev"} {
		if v := strings.TrimSpace(req.Params[key]); v != "" {
			xmlEdits[key] = v
		}
	}
	if len(xmlEdits) > 0 && isRunning {
		respond(w, false, "cpu_mode / video_model / boot_dev can only be changed while the VM is shut off — stop the VM and retry, or leave those fields unchanged.")
		return
	}
	if len(xmlEdits) > 0 {
		if err := applyXMLEdits(vmTarget, xmlEdits); err != nil {
			respond(w, false, err.Error())
			return
		}
		for k, v := range xmlEdits {
			msgs = append(msgs, k+"="+v+" (config; applies on next boot)")
		}
	}

	if m := strings.TrimSpace(req.Params["mem_mb"]); m != "" {
		mbN, convErr := strconv.Atoi(m)
		if convErr != nil {
			respond(w, false, "mem_mb must be an integer: "+convErr.Error())
			return
		}
		kib := fmt.Sprintf("%d", mbN*1024)

		// Operators think of "RAM" as a single value; libvirt
		// splits it into <memory> (max cap) + <currentMemory>
		// (balloon target). Always set BOTH --config — this keeps
		// the persisted config clean regardless of direction and
		// matches what the UI displays (persisted max).
		//
		// Order matters when crossing the current cap:
		//   raising → setmaxmem first, then setmem
		//   lowering → setmem first, then setmaxmem
		// Using "try setmaxmem, then setmem, fall back if either
		// fails" is brittle; instead we inspect the current max
		// and branch.
		curMaxKiB := currentMaxMemKiB(vmTarget)
		targetKiB := int64(mbN) * 1024
		if targetKiB > curMaxKiB {
			// Raising above current cap: setmaxmem first.
			if o, e := exec.Command("virsh", "setmaxmem", vmTarget, kib, "--config").CombinedOutput(); e != nil {
				respond(w, false, "setmaxmem failed: "+strings.TrimSpace(string(o)))
				return
			}
		}
		// Apply setmem (balloon target) — live when running, config
		// when stopped. Record whether --live took effect so we can
		// flag requires_reboot for the half that didn't.
		liveTook := false
		if isRunning {
			if _, e := exec.Command("virsh", "setmem", vmTarget, kib, "--config", "--live").CombinedOutput(); e == nil {
				liveTook = true
			} else if _, e2 := exec.Command("virsh", "setmem", vmTarget, kib, "--config").CombinedOutput(); e2 != nil {
				respond(w, false, "setmem --config failed: "+strings.TrimSpace(e2.Error()))
				return
			}
		} else {
			if o, e := exec.Command("virsh", "setmem", vmTarget, kib, "--config").CombinedOutput(); e != nil {
				respond(w, false, "setmem failed: "+strings.TrimSpace(string(o)))
				return
			}
		}
		if targetKiB < curMaxKiB {
			// Lowering: shrink the cap now that the balloon is at
			// or below it.
			if o, e := exec.Command("virsh", "setmaxmem", vmTarget, kib, "--config").CombinedOutput(); e != nil {
				respond(w, false, "setmaxmem (lower) failed: "+strings.TrimSpace(string(o)))
				return
			}
		}
		// Messaging: distinguish live+config (full apply now) from
		// config-only (reboot to apply cap change).
		switch {
		case !isRunning:
			msgs = append(msgs, "mem_mb="+m+" (config; applies on next boot)")
		case liveTook && targetKiB <= curMaxKiB:
			msgs = append(msgs, "mem_mb="+m+" (live + config)")
		case liveTook && targetKiB > curMaxKiB:
			msgs = append(msgs, "mem_mb="+m+" (cap raised + live)")
		default:
			msgs = append(msgs, "mem_mb="+m+" (config; reboot to apply)")
			requiresReboot = true
		}
	}
	if len(msgs) == 0 {
		respond(w, false, "EDIT_VM requires at least one of params.vcpus, params.mem_mb")
		return
	}
	// v5.27.7: build updated_fields from the post-edit dumpxml so
	// the server refreshes its cache immediately instead of
	// waiting for the next telemetry tick.
	updated := collectPostEditFields(vmTarget, req.Params)
	respondWithFlagsAndFields(w, true, "EDIT_VM: "+strings.Join(msgs, ", "), requiresReboot, updated)
}
