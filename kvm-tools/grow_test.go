//go:build linux || windows

package main

import (
	"context"
	"kvm-center/internal/growguest"
	"kvm-center/internal/growprotocol"
	"path/filepath"
	"testing"
)

func TestProductionGrowAlwaysDurablyRefuses(t *testing.T) {
	const vm = "11111111-1111-4111-8111-111111111111"
	g, e := growguest.OpenGuest(filepath.Join(t.TempDir(), "journal.sqlite"), vm)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	old := growGuest
	growGuest = g
	defer func() { growGuest = old }()
	r := growprotocol.Request{Version: 1, ID: "production-refusal", Session: g.Session(), Target: growprotocol.Target{VMUUID: vm, DiskSerial: "synthetic", FilesystemUUID: vm, FilesystemType: "ext4", Layout: "whole-disk"}, DesiredBytes: 8192}
	processGrowReply(vm, TelemetryReply{GrowRequestV1: &r, CanonicalUUID: "22222222-2222-4222-8222-222222222222"})
	p, e := g.Report(context.Background())
	if e != nil || len(p.Outcomes) != 0 {
		t.Fatal("canonical mismatch admitted")
	}
	processGrowReply(vm, TelemetryReply{GrowRequestV1: &r})
	processGrowReply(vm, TelemetryReply{GrowRequestV1: &r})
	p, e = g.Report(context.Background())
	if e != nil || len(p.Outcomes) != 1 || p.Outcomes[0].Status != "refused_native_unqualified" || p.Outcomes[0].NativeVerified {
		t.Fatal("production effect gate", p, e)
	}
}
