package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kvm-center/internal/models"
)

// installDeleteVMVirsh provides the only executable on PATH. All filesystem
// fixtures and command traces live in t.TempDir; no hypervisor is contacted.
func installDeleteVMVirsh(t *testing.T, domainXML, destroyOutput, undefineOutput string, destroyExit, undefineExit int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("virsh stub uses /bin/sh")
	}
	dir := t.TempDir()
	for name, data := range map[string]string{
		"domain.xml":   domainXML,
		"destroy.out":  destroyOutput,
		"undefine.out": undefineOutput,
		"calls":        "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> "$DELETE_VM_TEST_DIR/calls"
case "$1" in
  dumpxml) /bin/cat "$DELETE_VM_TEST_DIR/domain.xml" ;;
  destroy) /bin/cat "$DELETE_VM_TEST_DIR/destroy.out"; exit %d ;;
  undefine) /bin/cat "$DELETE_VM_TEST_DIR/undefine.out"; exit %d ;;
  *) echo 'unexpected virsh invocation' >&2; exit 97 ;;
esac
`, destroyExit, undefineExit)
	if err := os.WriteFile(filepath.Join(dir, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DELETE_VM_TEST_DIR", dir)
	t.Setenv("PATH", dir)
	return filepath.Join(dir, "calls")
}

func deleteVMTestResponse(t *testing.T, params map[string]string) models.CommandResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handleDeleteVM(w, models.CommandRequest{Action: "DELETE_VM", Params: params}, "test-vm")
	var result models.CommandResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v, body=%s", err, w.Body.String())
	}
	if (result.Success && w.Code != 200) || (!result.Success && w.Code < 400) {
		t.Fatalf("status %d disagrees with response %+v", w.Code, result)
	}
	return result
}

func readDeleteVMTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestDeleteVMRefusesUnownedStorageBeforeChanges(t *testing.T) {
	// Being attached to a domain, having a familiar filename, or appearing in
	// a storage pool is not an ownership record. Even a seemingly ordinary
	// local disk can be another VM's backing image or used on another host.
	for _, tc := range []struct {
		name string
		disk string
	}{
		{"ordinary file", "<disk type='file' device='disk'>\n<source file='%s'/>\n</disk>"},
		{"read only", "<disk type='file' device='disk'><readonly/>\n<source file='%s'/>\n</disk>"},
		{"cdrom", "<disk type='file' device='cdrom'>\n<source file='%s'/>\n</disk>"},
		{"floppy", "<disk type='file' device='floppy'>\n<source file='%s'/>\n</disk>"},
		{"shareable", "<disk type='file' device='disk'><shareable/>\n<source file='%s'/>\n</disk>"},
		{"backing image", "<disk type='file' device='disk'><backingStore type='file'>\n<source file='%s'/>\n</backingStore></disk>"},
		{"block device", "<disk type='block' device='disk'><source dev='%s'/></disk>"},
		{"network disk", "<disk type='network' device='disk'><source protocol='nfs' name='%s'/></disk>"},
		{"volume", "<disk type='volume' device='disk'><source pool='default' volume='%s'/></disk>"},
		{"double quoted", `<disk type="file" device="disk"><source file="%s"/></disk>`},
		{"malformed XML", "<disk type='file' device='disk'>\n<source file='%s'/>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test-vm.qcow2")
			if err := os.WriteFile(path, []byte("preserve this storage"), 0600); err != nil {
				t.Fatal(err)
			}
			xml := "<domain><name>test-vm</name><devices>" + fmt.Sprintf(tc.disk, path) + "</devices></domain>"
			calls := installDeleteVMVirsh(t, xml, "", "Domain test-vm has been undefined", 0, 0)
			result := deleteVMTestResponse(t, map[string]string{"delete_disks": "true"})
			if result.Success {
				t.Error("destructive deletion succeeded without proof of exclusive disk ownership")
			}
			if !strings.Contains(result.Message, "delete_disks=false") {
				t.Errorf("response should explain the safe retry: %q", result.Message)
			}
			if got := readDeleteVMTestFile(t, calls); got != "" {
				t.Errorf("VM must be left untouched on refused deletion; virsh calls: %q", got)
			}
			if got := readDeleteVMTestFile(t, path); got != "preserve this storage" {
				t.Errorf("storage changed: %q", got)
			}
		})
	}
}

func TestDeleteVMKeepDisks(t *testing.T) {
	for _, params := range []map[string]string{nil, {"delete_disks": ""}, {"delete_disks": "false"}} {
		t.Run(fmt.Sprintf("params=%v", params), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test-vm.qcow2")
			if err := os.WriteFile(path, []byte("keep disk"), 0600); err != nil {
				t.Fatal(err)
			}
			xml := fmt.Sprintf("<domain><devices><disk type='file' device='disk'>\n<source file='%s'/>\n</disk></devices></domain>", path)
			calls := installDeleteVMVirsh(t, xml, "", "Domain test-vm has been undefined", 0, 0)
			result := deleteVMTestResponse(t, params)
			if !result.Success {
				t.Fatalf("keep-disks deletion failed: %+v", result)
			}
			if !strings.Contains(result.Message, "Disk files retained") {
				t.Errorf("success response must disclose retained files: %q", result.Message)
			}
			want := "destroy test-vm\nundefine test-vm --nvram --managed-save --snapshots-metadata\n"
			if got := readDeleteVMTestFile(t, calls); got != want {
				t.Errorf("virsh calls = %q, want %q", got, want)
			}
			if got := readDeleteVMTestFile(t, path); got != "keep disk" {
				t.Errorf("disk changed: %q", got)
			}
		})
	}
}

func TestDeleteVMKeepDisksFailures(t *testing.T) {
	for _, tc := range []struct {
		name, destroyOutput, undefineOutput string
		destroyExit, undefineExit           int
		wantSuccess, wantUndefine           bool
	}{
		{"destroy failed", "connection refused", "", 1, 0, false, false},
		{"not running", "error: domain is not running", "undefined", 1, 0, true, true},
		{"inactive", "error: domain is not active", "undefined", 1, 0, true, true},
		{"undefine failed", "", "cannot undefine domain", 0, 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := installDeleteVMVirsh(t, "", tc.destroyOutput, tc.undefineOutput, tc.destroyExit, tc.undefineExit)
			result := deleteVMTestResponse(t, map[string]string{"delete_disks": "false"})
			if result.Success != tc.wantSuccess {
				t.Errorf("result = %+v, want success=%t", result, tc.wantSuccess)
			}
			want := "destroy test-vm\n"
			if tc.wantUndefine {
				want += "undefine test-vm --nvram --managed-save --snapshots-metadata\n"
			}
			if got := readDeleteVMTestFile(t, calls); got != want {
				t.Errorf("virsh calls = %q, want %q", got, want)
			}
		})
	}
}

func TestDeleteVMRefusesInvalidStorageFlagBeforeChanges(t *testing.T) {
	for _, value := range []string{"TRUE", "1", "yes", "false ", "true ", "unknown"} {
		t.Run(value, func(t *testing.T) {
			calls := installDeleteVMVirsh(t, "", "", "", 0, 0)
			result := deleteVMTestResponse(t, map[string]string{"delete_disks": value})
			if result.Success || !strings.Contains(result.Message, "invalid delete_disks") {
				t.Errorf("invalid parameter was not rejected: %+v", result)
			}
			if got := readDeleteVMTestFile(t, calls); got != "" {
				t.Errorf("invalid request must not change the VM; virsh calls: %q", got)
			}
		})
	}
}

func TestDeleteVMRefusalRecordedAsFailure(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	calls := installDeleteVMVirsh(t, "", "", "", 0, 0)
	req := models.CommandRequest{
		RequestID: "req_delete_unowned_disk", Action: "DELETE_VM", VMName: "test-vm",
		Params: map[string]string{"delete_disks": "true"},
	}
	if _, _, err := claimCommand(req); err != nil {
		t.Fatal(err)
	}
	// Run the async body synchronously: no goroutine or real virsh needed.
	runCommandBodyAsync(req)
	got, err := s.Get(context.Background(), req.RequestID)
	if err != nil || got == nil {
		t.Fatalf("read operation: %v, operation=%+v", err, got)
	}
	if got.State != OpStateFailed || got.CompletedAt == nil {
		t.Errorf("refusal must be a terminal failure: %+v", got)
	}
	status, body, _, ok := outcomes.lookup(req.RequestID)
	if !ok || status < 400 || !strings.Contains(string(body), "delete_disks=false") {
		t.Errorf("refusal not cached as failure: ok=%t, status=%d, body=%s", ok, status, body)
	}
	if got := readDeleteVMTestFile(t, calls); got != "" {
		t.Errorf("refused async deletion mutated VM: %q", got)
	}
}
