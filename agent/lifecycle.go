package main

// lifecycle.go (v5.29.0): watches VM power-state changes and triggers
// an immediate telemetry POST when anything flips. Complements the
// 15 s HTTP tick + the 3 s gRPC metrics stream:
//
//   - HTTP telemetry (15 s): authoritative inventory
//   - gRPC live metrics (3 s): numeric gauges (CPU/mem)
//   - lifecycle watcher (2 s): state flips → immediate full telemetry
//
// Without this, a VM that goes running → shut off wouldn't reflect in
// the UI for up to 15 s. Poll cost is negligible — `virsh list --all`
// is a local libvirt call that takes <10 ms on a homelab host.

import (
	"context"
	"log"
	"time"

	"kvm-center/internal/models"
)

const lifecyclePollInterval = 2 * time.Second

// startLifecycleWatcher fires sendTelemetry immediately when any VM's
// state changes between polls. Runs until ctx is cancelled.
func startLifecycleWatcher(ctx context.Context, serverURL string, agentPort int) {
	prev := snapshotVMStates()
	ticker := time.NewTicker(lifecyclePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := snapshotVMStates()
			if !sameVMStates(prev, cur) {
				log.Printf("lifecycle: VM state change detected — firing immediate telemetry")
				sendTelemetry(serverURL, agentPort)
				prev = cur
			}
		}
	}
}

func snapshotVMStates() map[string]models.VMState {
	m := make(map[string]models.VMState)
	for _, vm := range listVMs() {
		key := vm.UUID
		if key == "" {
			key = vm.Name
		}
		m[key] = vm.State
	}
	return m
}

func sameVMStates(a, b map[string]models.VMState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || va != vb {
			return false
		}
	}
	return true
}
