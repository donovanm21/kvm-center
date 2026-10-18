package nativevalidation

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"kvm-center/internal/nativework"
)

func mutationFixture(t *testing.T, body string) (Virsh, *nativework.Journal, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic subprocess")
	}
	root := privateDir(t)
	binary := filepath.Join(root, "fake-virsh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\" >> \"$HOME/spawns\"\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	socketRoot, err := os.MkdirTemp("/tmp", "kvwm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketRoot) })
	socket := filepath.Join(socketRoot, "socket")
	oldEndpoint := powerEndpoint
	powerEndpoint = socket
	t.Cleanup(func() { powerEndpoint = oldEndpoint })
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	path := filepath.Join(root, "writers.sqlite")
	j, err := nativework.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return Virsh{Binary: binary, Socket: socket, Home: root}, j, path
}
func tracked(t *testing.T, v Virsh, j *nativework.Journal, request, action string) (Virsh, *VirshMutation) {
	t.Helper()
	v, m, err := TrackVirshMutation(v, j, testID, request, request+"-attempt", action)
	if err != nil {
		t.Fatal(err)
	}
	return v, m
}
func unresolved(t *testing.T, j *nativework.Journal, n int) {
	t.Helper()
	e, err := j.Evidence(context.Background(), nativework.Target{UUID: testID.UUID, Name: testID.Name})
	if err != nil {
		t.Fatal(err)
	}
	if e.RegisteredWriters != n || e.RegisteredWritersUnresolved != n || e.RegisteredWriterDrainVerified || e.NativeQuiescenceVerified || e.WriterCoverageComplete {
		t.Fatalf("unexpected evidence: %+v", e)
	}
}
func noSpawn(t *testing.T, v Virsh) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(v.Home, "spawns")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected spawn", err)
	}
}
func awaitSpawn(t *testing.T, v Virsh) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(v.Home, "spawns")); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no subprocess start")
		case <-tick.C:
		}
	}
}

func TestTrackedMutationReapedNeverResolvesNativeEffects(t *testing.T) {
	for _, action := range []string{"define", "start", "destroy", "undefine"} {
		t.Run(action, func(t *testing.T) {
			v, j, _ := mutationFixture(t, "exit 0")
			v, m := tracked(t, v, j, "req", action)
			var err error
			switch action {
			case "define":
				err = v.Define(context.Background(), XML(testID))
			case "start":
				err = v.Start(context.Background(), testID.UUID)
			case "destroy":
				err = v.Destroy(context.Background(), testID.UUID)
			case "undefine":
				err = v.Undefine(context.Background(), testID.UUID)
			}
			if !errors.Is(err, ErrNativeEffectsUnresolved) {
				t.Fatal(err)
			}
			r := m.Receipt()
			if !r.Registered || !r.Started || !r.Reaped || r.ExitCode != 0 || r.NativeEffectsResolved || r.Writer.Request.ID != "req" || r.Writer.ParentID == "" {
				t.Fatalf("receipt: %+v", r)
			}
			unresolved(t, j, 2)
			if err = j.Seal(context.Background(), r.Writer.Request.Target); err != nil {
				t.Fatal(err)
			}
			unresolved(t, j, 2)
		})
	}
}
func TestUntrackedAndMismatchedMutationNeverSpawns(t *testing.T) {
	v, j, _ := mutationFixture(t, "exit 0")
	for _, call := range []func(context.Context, string) error{v.Define, v.Start, v.Destroy, v.Undefine} {
		if !errors.Is(call(context.Background(), testID.UUID), ErrMutationTrackingRequired) {
			t.Fatal("missing tracking accepted")
		}
	}
	noSpawn(t, v)
	for _, kind := range []string{"action", "uuid", "binary", "socket", "home", "xml"} {
		t.Run(kind, func(t *testing.T) {
			vv, m := tracked(t, v, j, "req-"+kind, "start")
			id := testID.UUID
			switch kind {
			case "uuid":
				id = "00000000-0000-4000-8000-000000000001"
			case "binary":
				vv.Binary += "-other"
			case "socket":
				vv.Socket += "-other"
			case "home":
				vv.Home += "-other"
			}
			var err error
			switch kind {
			case "action":
				err = vv.Destroy(context.Background(), id)
			case "xml":
				err = vv.Define(context.Background(), "<domain/>")
			default:
				err = vv.Start(context.Background(), id)
			}
			if err == nil || m.Receipt().Registered {
				t.Fatal("binding mismatch accepted", err)
			}
			noSpawn(t, v)
			if !errors.Is(vv.Start(context.Background(), testID.UUID), nativework.ErrReplay) {
				t.Fatal("failed tracker was reset")
			}
		})
	}
}
func TestMutationRetryRestartAndFailedAdmission(t *testing.T) {
	v, j, path := mutationFixture(t, "exit 0")
	vv, _ := tracked(t, v, j, "req", "start")
	_ = vv.Start(context.Background(), testID.UUID)
	for _, id := range []string{"req", "other"} {
		n, _ := tracked(t, v, j, id, "start")
		err := n.Start(context.Background(), testID.UUID)
		if !errors.Is(err, nativework.ErrReplay) && !errors.Is(err, nativework.ErrBlocked) {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopen, err := nativework.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopen.Close()
	unresolved(t, reopen, 2)
	n, m := tracked(t, v, reopen, "after-restart", "start")
	if err = n.Start(context.Background(), testID.UUID); !errors.Is(err, nativework.ErrBlocked) || m.Receipt().Started {
		t.Fatal("restart admitted", err)
	}
	raw, _ := os.ReadFile(filepath.Join(v.Home, "spawns"))
	if string(raw) != "start\n" {
		t.Fatalf("retry dispatched: %q", raw)
	}
	t.Run("closed journal", func(t *testing.T) {
		v, j, _ := mutationFixture(t, "exit 0")
		vv, _ := tracked(t, v, j, "req", "start")
		j.Close()
		if vv.Start(context.Background(), testID.UUID) == nil {
			t.Fatal("closed journal accepted")
		}
		noSpawn(t, v)
	})
}
func TestMutationConcurrentInvocationAndCancellation(t *testing.T) {
	v, j, _ := mutationFixture(t, "exec /bin/sleep 10")
	v, m := tracked(t, v, j, "req", "start")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- v.Start(ctx, testID.UUID) }()
	awaitSpawn(t, v)
	unresolved(t, j, 2)
	if m.Receipt().Reaped {
		t.Fatal("reported reaped before wait")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !errors.Is(v.Start(context.Background(), testID.UUID), nativework.ErrReplay) {
				t.Error("overlapping invocation accepted")
			}
		}()
	}
	wg.Wait()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !m.Receipt().Reaped {
		t.Fatal("exact process not reaped")
	}
	unresolved(t, j, 2)
	raw, _ := os.ReadFile(filepath.Join(v.Home, "spawns"))
	if string(raw) != "start\n" {
		t.Fatalf("concurrent dispatch: %q", raw)
	}
}
func TestFailedStartAndExitRemainUnresolved(t *testing.T) {
	for _, kind := range []string{"start", "exit", "diagnostics", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			body := "exit 9"
			if kind == "diagnostics" {
				body = "printf warning >&2; exit 0"
			}
			if kind == "overflow" {
				body = "i=0; while [ $i -lt 18000 ]; do printf 'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz0123456789\\n'; i=$((i+1)); done"
			}
			v, j, _ := mutationFixture(t, body)
			v, m := tracked(t, v, j, "req", "start")
			if kind == "start" {
				if err := os.Remove(v.Binary); err != nil {
					t.Fatal(err)
				}
			}
			if v.Start(context.Background(), testID.UUID) == nil {
				t.Fatal("uncertainty accepted")
			}
			unresolved(t, j, 2)
			r := m.Receipt()
			if kind == "start" {
				if r.Started || r.Reaped {
					t.Fatal(r)
				}
			} else if !r.Reaped {
				t.Fatal("wait not recorded", r)
			}
		})
	}
}
func TestUnqualifiedNativeConsumersRefuseBeforeEffects(t *testing.T) {
	v, j, _ := mutationFixture(t, "exit 0")
	v, _ = tracked(t, v, j, "req", "define")
	save := func(Journal) error { t.Fatal("consumer persisted before readiness"); return nil }
	if _, err := Prepare(context.Background(), v, testID, "01234567890123456789012345678901", save); !errors.Is(err, ErrNativeEffectsUnresolved) {
		t.Fatal(err)
	}
	if _, err := Recover(v, Journal{Identity: testID, Pending: true}, save); !errors.Is(err, ErrNativeEffectsUnresolved) {
		t.Fatal(err)
	}
	for _, recovery := range []bool{false, true} {
		if err := RunUIWatchdog(context.Background(), UIWatchConfig{}, v, recovery, nil); !errors.Is(err, ErrNativeEffectsUnresolved) {
			t.Fatal(err)
		}
	}
	noSpawn(t, v)
}

func TestMutationActiveRestartAndLostResponseKeepFence(t *testing.T) {
	v, j, path := mutationFixture(t, "exec /bin/sleep 10")
	v, m := tracked(t, v, j, "req", "start")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- v.Start(ctx, testID.UUID) }()
	awaitSpawn(t, v)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := nativework.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	unresolved(t, recovered, 2)
	base := Virsh{Binary: v.Binary, Socket: v.Socket, Home: v.Home}
	retry, _ := tracked(t, base, recovered, "replacement", "destroy")
	if err = retry.Destroy(context.Background(), testID.UUID); !errors.Is(err, nativework.ErrBlocked) {
		t.Fatal("recovery cleanup admitted", err)
	}
	cancel()
	<-done // Simulate a lost caller response; no completion is imported.
	if !m.Receipt().Reaped {
		t.Fatal("old live owner failed to reap")
	}
	unresolved(t, recovered, 2)
	raw, _ := os.ReadFile(filepath.Join(v.Home, "spawns"))
	if string(raw) != "start\n" {
		t.Fatalf("restart redispatched %q", raw)
	}
}

func TestMutationDescendantAndDelayedWaitNeverAuthorizeDrain(t *testing.T) {
	// The direct shell exits while an untracked child retains stdout. Wait must
	// finish its bounded pipe handling before Reaped is exposed. Neither that
	// receipt nor eventual descendant exit can resolve either native writer.
	v, j, _ := mutationFixture(t, "/bin/sleep 2 &\nexit 0")
	v, m := tracked(t, v, j, "req", "start")
	done := make(chan error, 1)
	go func() { done <- v.Start(context.Background(), testID.UUID) }()
	awaitSpawn(t, v)
	if m.Receipt().Reaped {
		t.Fatal("reported wait completion early")
	}
	unresolved(t, j, 2)
	if err := <-done; !errors.Is(err, ErrNativeEffectsUnresolved) {
		t.Fatal(err)
	}
	if !m.Receipt().Reaped {
		t.Fatal("direct process not reaped")
	}
	unresolved(t, j, 2)
}

func TestHiddenOrAbsentReadinessCannotActivateConsumers(t *testing.T) {
	v, j, _ := mutationFixture(t, "exit 0")
	v, _ = tracked(t, v, j, "req", "define")
	for _, d := range []Driver{v, struct{ Driver }{v}} {
		save := func(Journal) error { t.Fatal("consumer persisted before readiness"); return nil }
		if _, err := Prepare(context.Background(), d, testID, testSecret, save); !errors.Is(err, ErrNativeEffectsUnresolved) {
			t.Fatal(err)
		}
		if _, err := Recover(d, Journal{Identity: testID, Pending: true}, save); !errors.Is(err, ErrNativeEffectsUnresolved) {
			t.Fatal(err)
		}
		for _, recovery := range []bool{false, true} {
			if err := RunUIWatchdog(context.Background(), UIWatchConfig{}, d, recovery, nil); !errors.Is(err, ErrNativeEffectsUnresolved) {
				t.Fatal(err)
			}
			if err := Run(context.Background(), Config{CleanupOnly: recovery}, d, nil); !errors.Is(err, ErrNativeEffectsUnresolved) {
				t.Fatal(err)
			}
		}
	}
	noSpawn(t, v)
}

func TestMutationOwnerChangeCannotReplaceCanonicalJournal(t *testing.T) {
	v, j, _ := mutationFixture(t, "exit 0")
	first, m1 := tracked(t, v, j, "req", "start")
	_ = first.Start(context.Background(), testID.UUID)
	secondJournal, err := nativework.Open(filepath.Join(v.Home, "other-writers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer secondJournal.Close()
	other := testID
	other.Owner = strings.Repeat("b", 64)
	if other.Owner == testID.Owner {
		t.Fatal("test owner collision")
	}
	second, m2, err := TrackVirshMutation(v, secondJournal, other, "req", "req-attempt", "start")
	if err != nil {
		t.Fatal(err)
	}
	err = second.Start(context.Background(), testID.UUID)
	if err == nil || !m1.Receipt().Registered || m2.Receipt().Registered {
		t.Fatal("owner change replaced canonical predecessor", err)
	}
	unresolved(t, j, 2)
	raw, readErr := os.ReadFile(filepath.Join(v.Home, "spawns"))
	if readErr != nil || string(raw) != "start\n" {
		t.Fatal("alternate journal spawned", string(raw), readErr)
	}
}
