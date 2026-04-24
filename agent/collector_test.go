package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestPrevVMCPUConcurrent exercises the prevVMCPUMu invariant under
// parallel access — two writer goroutines churning the map via
// forgetVMCPU + direct updates + a reader goroutine via sweepVMCPU.
// Running under `go test -race ./agent` catches any regression of
// the mutex guard that was added post-v5.32.14.
//
// The vmCPUUsage path itself shells out to virsh so it isn't
// directly covered here; the test locks down the surrounding
// primitives that the CPU-percent path relies on.
func TestPrevVMCPUConcurrent(t *testing.T) {
	// Start from a known state so parallel test runs don't see
	// entries from sibling tests.
	prevVMCPUMu.Lock()
	for k := range prevVMCPU {
		delete(prevVMCPU, k)
	}
	prevVMCPUMu.Unlock()

	const workers = 8
	const perWorker = 500
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				name := fmt.Sprintf("vm-%d-%d", w, i%16)
				// Write directly — exactly what vmCPUUsage does post-lock.
				prevVMCPUMu.Lock()
				prevVMCPU[name] = struct {
					cpuNs uint64
					ts    time.Time
				}{uint64(i), time.Now()}
				prevVMCPUMu.Unlock()
				if i%4 == 0 {
					forgetVMCPU(name)
				}
				if i%16 == 0 {
					running := map[string]struct{}{
						fmt.Sprintf("vm-%d-%d", w, i%16): {},
					}
					sweepVMCPU(running)
				}
			}
		}(w)
	}
	wg.Wait()

	// After the churn the map is still operable.
	forgetVMCPU("non-existent")
	sweepVMCPU(map[string]struct{}{})
	prevVMCPUMu.Lock()
	if len(prevVMCPU) != 0 {
		t.Fatalf("sweepVMCPU with empty running set should have cleared the map; %d entries remain", len(prevVMCPU))
	}
	prevVMCPUMu.Unlock()
}

// TestForgetVMCPURemovesSingleEntry locks the pre-v5.32.14 fix:
// lifecycle-watcher calls forgetVMCPU when a VM flips away from
// running. The entry must be gone; the next start recomputes from
// a fresh baseline instead of subtracting against an old cpuNs
// (which would underflow uint64).
func TestForgetVMCPURemovesSingleEntry(t *testing.T) {
	prevVMCPUMu.Lock()
	prevVMCPU["vm-a"] = struct {
		cpuNs uint64
		ts    time.Time
	}{1_000_000, time.Now()}
	prevVMCPU["vm-b"] = struct {
		cpuNs uint64
		ts    time.Time
	}{2_000_000, time.Now()}
	prevVMCPUMu.Unlock()

	forgetVMCPU("vm-a")

	prevVMCPUMu.Lock()
	defer prevVMCPUMu.Unlock()
	if _, ok := prevVMCPU["vm-a"]; ok {
		t.Fatal("forgetVMCPU should have removed vm-a")
	}
	if _, ok := prevVMCPU["vm-b"]; !ok {
		t.Fatal("forgetVMCPU must not touch other entries")
	}
	// Cleanup so sibling tests start from a known state.
	delete(prevVMCPU, "vm-b")
}

// TestSweepVMCPUEvicts verifies the sweep drops entries for names
// no longer in the running set, without dropping ones that are.
// The periodic sweep is the belt-and-braces eviction layer that
// catches VMs which disappeared between telemetry ticks (deleted
// / renamed) — forgetVMCPU handles the common stop transition but
// misses rename-on-migration.
func TestSweepVMCPUEvicts(t *testing.T) {
	prevVMCPUMu.Lock()
	for k := range prevVMCPU {
		delete(prevVMCPU, k)
	}
	prevVMCPU["alive"] = struct {
		cpuNs uint64
		ts    time.Time
	}{100, time.Now()}
	prevVMCPU["gone"] = struct {
		cpuNs uint64
		ts    time.Time
	}{200, time.Now()}
	prevVMCPUMu.Unlock()

	sweepVMCPU(map[string]struct{}{"alive": {}})

	prevVMCPUMu.Lock()
	defer prevVMCPUMu.Unlock()
	if _, ok := prevVMCPU["alive"]; !ok {
		t.Fatal("sweep dropped the running entry")
	}
	if _, ok := prevVMCPU["gone"]; ok {
		t.Fatal("sweep failed to drop the stopped entry")
	}
	delete(prevVMCPU, "alive")
}
