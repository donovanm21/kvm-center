package main

import (
	"context"
	"encoding/json"
	"kvm-center/internal/growguest"
	"kvm-center/internal/growprotocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func growTestRequest(session string) growprotocol.Request {
	return growprotocol.Request{Version: 1, ID: "grow-one", Session: session, Target: growprotocol.Target{VMUUID: updateVM1, DiskSerial: "synthetic-disk", FilesystemUUID: updateVM2, FilesystemType: "ext4", Layout: "whole-disk"}, DesiredBytes: 8192}
}
func TestGrowProtocolDurableExchangeAndRestore(t *testing.T) {
	s := updateStore(t, nil)
	gpath := filepath.Join(t.TempDir(), "guest.sqlite")
	g, e := growguest.OpenGuest(gpath, updateVM1)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	report, e := g.Report(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	req := growTestRequest(g.Session())
	if s.QueueGrow(req) == nil {
		t.Fatal("admitted without live capability")
	}
	if offer, e := s.ProcessGrowReport(updateVM1, report); e != nil || offer != nil {
		t.Fatal(e)
	}
	if e := s.QueueGrow(req); e != nil {
		t.Fatal(e)
	}
	queued, e := s.ExportSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	offer, e := s.ProcessGrowReport(updateVM1, report)
	if e != nil || offer == nil || *offer != req {
		t.Fatal("missing offer", e)
	}
	// Server restart may retransmit the immutable offer to the same guest session.
	s.saveWG.Wait()
	s = NewStoreWithBackend(s.backend, "")
	s.saveWG.Wait()
	offer, e = s.ProcessGrowReport(updateVM1, report)
	if e != nil || offer == nil || *offer != req {
		t.Fatal("lost response not replayed", e)
	}
	o, e := g.Run(context.Background(), *offer, nil)
	if e != nil || o.Status != "refused_native_unqualified" {
		t.Fatal(e, o)
	}
	// Guest restart retains outcome but rotates offer capability.
	g.Close()
	g, e = growguest.OpenGuest(gpath, updateVM1)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	report, e = g.Report(context.Background())
	if e != nil || report.Session == req.Session {
		t.Fatal("session survived", e)
	}
	if offer, e = s.ProcessGrowReport(updateVM1, report); e != nil || offer != nil {
		t.Fatal("outcome not correlated", e)
	}
	if offer, e = s.ProcessGrowReport(updateVM1, report); e != nil || offer != nil {
		t.Fatal("duplicate result refused", e)
	}
	bad := report
	bad.Outcomes = append([]growprotocol.Outcome{}, report.Outcomes...)
	bad.Outcomes[0].Status = "recovery_required"
	if _, e = s.ProcessGrowReport(updateVM1, bad); e == nil {
		t.Fatal("conflicting response accepted")
	}
	bad.Outcomes[0] = o
	bad.Outcomes[0].Digest = strings.Repeat("b", 64)
	if _, e = s.ProcessGrowReport(updateVM1, bad); e == nil {
		t.Fatal("stale response accepted")
	}
	n := req
	n.ID = "replacement"
	n.Session = report.Session
	if s.QueueGrow(n) == nil {
		t.Fatal("new ID bypass")
	}
	final, e := s.ExportSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	for _, snapshot := range [][]byte{queued, final} {
		path := filepath.Join(t.TempDir(), "restored.json")
		if e = os.WriteFile(path, snapshot, 0600); e != nil {
			t.Fatal(e)
		}
		restored := NewStoreWithBackend(NewJSONBackend(path), "")
		restored.saveWG.Wait()
		empty := growprotocol.Report{Version: 1, Session: req.Session, Outcomes: []growprotocol.Outcome{}}
		if offer, e = restored.ProcessGrowReport(updateVM1, empty); e != nil || offer != nil {
			t.Fatal("restored queued/completed request offered", e)
		}
		var d persistData
		if e = json.Unmarshal(snapshot, &d); e != nil {
			t.Fatal(e)
		}
		back, e := restored.backend.Load(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if e = verifyBackendRoundTrip(context.Background(), &d, restored.backend); e != nil {
			t.Fatal(e)
		}
		if len(back.GuestGrows.Records) != 1 {
			t.Fatal("lost ledger")
		}
	}
}
func TestGrowProtocolUnsupportedAndStaleGuest(t *testing.T) {
	s := updateStore(t, nil)
	session := strings.Repeat("a", 64)
	p := growprotocol.Report{Version: 1, Session: session, Outcomes: []growprotocol.Outcome{}}
	s.ProcessGrowReport(updateVM1, p)
	r := growTestRequest(session)
	if e := s.QueueGrow(r); e != nil {
		t.Fatal(e)
	}
	s.ProcessGrowReport(updateVM1, p)
	fresh := p
	fresh.Session = strings.Repeat("b", 64)
	if offer, e := s.ProcessGrowReport(updateVM1, fresh); e != nil || offer != nil {
		t.Fatal("stale request delivered to restarted guest")
	}
	wrong := p
	wrong.Outcomes = []growprotocol.Outcome{growprotocol.Result(r, "refused_identity", 0)}
	if _, e := s.ProcessGrowReport(updateVM2, wrong); e == nil {
		t.Fatal("wrong VM acknowledged")
	}
	s.mu.Lock()
	s.guestGrowUncertain = true
	s.mu.Unlock()
	if _, e := s.ExportSnapshot(); e == nil {
		t.Fatal("uncertain export")
	}
	if offer, e := s.ProcessGrowReport(updateVM1, p); e == nil || offer != nil {
		t.Fatal("uncertain offered")
	}
}

func TestGrowServerRejectsGuestPhase(t *testing.T) {
	r := growTestRequest(strings.Repeat("a", 64))
	state := growprotocol.State{Version: 1, Records: []growprotocol.Record{{Request: r, Phase: "reserved"}}}
	raw, _ := json.Marshal(map[string]any{"guest_grow_state": state})
	var d persistData
	if json.Unmarshal(raw, &d) == nil {
		t.Fatal("server accepted guest phase")
	}
}
