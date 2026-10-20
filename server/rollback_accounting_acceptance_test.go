package main

import (
	"bytes"
	"encoding/json"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"kvm-center/internal/rollbackaccounting"
)

func compositionAccountingInput(t *testing.T, d *persistData, revision, artifact string, originals map[string]string, indexes map[string][]byte) rollbackaccounting.Input {
	t.Helper()
	raw, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	var sections map[string]json.RawMessage
	if e = json.Unmarshal(raw, &sections); e != nil {
		t.Fatal(e)
	}
	in := rollbackaccounting.Input{Schema: rollbackaccounting.Schema, SourceRevision: revision, ArtifactSHA256: artifact, OriginalHashes: originals, Sections: sections, Collections: map[string][][]byte{}, Indexes: indexes}
	if d.EntityOverlay == nil || len(d.EntityOverlay.Collections) != 13 {
		t.Fatal("accounting overlay scope")
	}
	for name, records := range d.EntityOverlay.Collections {
		in.Collections[name] = [][]byte{}
		ids := make([]string, 0, len(records))
		for id := range records {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			in.Collections[name] = append(in.Collections[name], records[id])
		}
	}
	return in
}

// Verify the exact documented writes and generated bookkeeping before reporting
// their byte-level deltas. Every other application snapshot section must match.
func checkCompositionWriteAccounting(t *testing.T, before, after *persistData, a, b rollbackaccounting.Input, started, finished time.Time) *rollbackaccounting.Report {
	t.Helper()
	encoded, e := json.Marshal(before)
	if e != nil {
		t.Fatal(e)
	}
	var want persistData
	if e = json.Unmarshal(encoded, &want); e != nil {
		t.Fatal(e)
	}
	want.VMNotes[legacyfixture.VMID] = "composition durable note"
	for _, vms := range want.VMs {
		for _, vm := range vms {
			if vm.UUID == legacyfixture.VMID {
				vm.Notes = "composition durable note"
			}
		}
	}
	want.Groups["fixture-group"].Description = "composition durable group"
	want.Alarms[0].Name = "composition durable alarm"
	stamp, e := time.Parse(time.RFC3339, after.Alarms[0].UpdatedAt)
	if e != nil || stamp.Before(started.Truncate(time.Second)) || stamp.After(finished) {
		t.Fatal("unaccounted alarm timestamp")
	}
	want.Alarms[0].UpdatedAt = after.Alarms[0].UpdatedAt
	want.VMTagState.ByVM[legacyfixture.VMID] = []string{"composed", "database"}
	tokenID := ""
	for id, v := range want.APITokens {
		if v.Name == "composition acceptance" {
			if tokenID != "" {
				t.Fatal("ambiguous acceptance token")
			}
			tokenID = id
			actual := after.APITokens[id]
			if actual == nil || actual.LastUsedAt.Before(v.LastUsedAt) || actual.LastUsedAt.After(finished) {
				t.Fatal("unaccounted token timestamp")
			}
			v.LastUsedAt = actual.LastUsedAt
		}
	}
	if tokenID == "" {
		t.Fatal("missing acceptance token")
	}
	existing := map[string]*models.AuditEvent{}
	for _, v := range before.Audit {
		existing[v.ID] = v
	}
	actions := map[string]string{AuditVMNotes: "vm/" + legacyfixture.VMID, AuditGroupUpdate: "group/fixture-group", "vm.tags.updated": "vm/" + legacyfixture.VMID, AuditBackupExport: "backup"}
	added := map[string]bool{}
	for _, ev := range after.Audit {
		if old := existing[ev.ID]; old != nil {
			if !reflect.DeepEqual(old, ev) {
				t.Fatal("prior audit changed")
			}
			delete(existing, ev.ID)
			continue
		}
		resource, ok := actions[ev.Action]
		if !ok || ev.Resource != resource || ev.ActorID != "fixture-reader" || ev.ActorName != "fixture-reader" || !ev.Success || ev.Error != "" || ev.At.Before(started) || ev.At.After(finished) || !net.ParseIP(ev.ActorIP).IsLoopback() {
			t.Fatal("unaccounted generated audit")
		}
		detail := map[string]string{"via": "token:" + tokenID, "token_scopes": strings.Join(want.APITokens[tokenID].Scopes, ",")}
		switch ev.Action {
		case AuditVMNotes:
			detail["chars"] = strconv.Itoa(len("composition durable note"))
		case "vm.tags.updated":
			detail["tags"] = "composed,database"
		case AuditBackupExport:
			detail["actor"] = "fixture-reader"
			for _, k := range []string{"files", "store_bytes"} {
				n, e := strconv.Atoi(ev.Detail[k])
				if e != nil || n <= 0 {
					t.Fatal("unaccounted backup audit size")
				}
				detail[k] = ev.Detail[k]
			}
		}
		if !reflect.DeepEqual(detail, ev.Detail) {
			t.Fatal("unaccounted audit details")
		}
		delete(actions, ev.Action)
		added[rollbackaccounting.Hash([]byte(ev.ID))] = true
	}
	if len(existing) != 0 || len(actions) != 0 {
		t.Fatal("missing audit accounting")
	}
	want.Audit = after.Audit
	if len(after.CommandLog) != len(before.CommandLog)+1 {
		t.Fatal("unaccounted task count")
	}
	if want.CommandLog == nil {
		want.CommandLog = map[string]*models.CommandLogEntry{}
	}
	for id, entry := range after.CommandLog {
		if old := before.CommandLog[id]; old != nil {
			if !reflect.DeepEqual(old, entry) {
				t.Fatal("prior task changed")
			}
			continue
		}
		if !strings.HasPrefix(id, "srv-"+AuditGroupUpdate+"-") || entry.SubmittedAt.Before(started) || entry.SubmittedAt.After(finished) {
			t.Fatal("unaccounted generated task")
		}
		stamp := entry.SubmittedAt
		expected := &models.CommandLogEntry{RequestID: id, Action: AuditGroupUpdate, ActorID: "fixture-reader", SubmittedAt: stamp, AppliedAt: &stamp, Outcome: "success"}
		if !reflect.DeepEqual(entry, expected) {
			t.Fatal("unaccounted task fields")
		}
		want.CommandLog[id] = entry
	}
	// Raw overlay changes are independently checked below; all typed sections,
	// including private storage fields, must otherwise match the explicit plan.
	want.EntityOverlay = after.EntityOverlay
	x, e := json.Marshal(&want)
	if e != nil {
		t.Fatal(e)
	}
	y, e := json.Marshal(after)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(x, y) {
		t.Fatal("unexplained application snapshot delta")
	}
	report, e := rollbackaccounting.Compare(a, b)
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Indexes) != 0 {
		t.Fatal("unaccounted index change")
	}
	allowed := map[string]map[string]bool{}
	allow := func(col, id string, fields ...string) {
		key := col + "/" + rollbackaccounting.Hash([]byte(id))
		allowed[key] = map[string]bool{}
		for _, field := range fields {
			allowed[key][rollbackaccounting.Hash([]byte(field))] = true
		}
	}
	allow("vms", legacyfixture.VMID, "notes")
	allow("vm_notes", legacyfixture.VMID, "notes")
	allow("groups", "fixture-group", "description")
	allow("alarms", "composition-alarm", "name", "updatedat")
	allow("api_tokens", tokenID, "lastusedat")
	for _, col := range report.Collections {
		if !col.BeforePresent || !col.AfterPresent || col.Deleted != 0 {
			t.Fatal("unaccounted entity inventory")
		}
		for _, rec := range col.Records {
			if rec.Before == rec.After {
				continue
			}
			if !rec.Before.Present {
				if col.Name != "audit_events" || !added[rec.IdentitySHA256] {
					t.Fatal("unaccounted added entity")
				}
				known := map[string]bool{}
				for _, key := range []string{"_id", "id", "at", "actor_id", "actorname", "actorip", "action", "resource", "detail", "success", "error"} {
					known[rollbackaccounting.Hash([]byte(key))] = true
				}
				for _, field := range rec.Fields {
					if !known[field.KeySHA256] {
						t.Fatal("unaccounted generated audit BSON field")
					}
				}
				delete(added, rec.IdentitySHA256)
				continue
			}
			fields, ok := allowed[col.Name+"/"+rec.IdentitySHA256]
			if !ok || len(rec.Fields) == 0 {
				t.Fatal("unaccounted entity edit")
			}
			for _, field := range rec.Fields {
				if !fields[field.KeySHA256] {
					t.Fatal("unaccounted raw field change")
				}
			}
		}
	}
	if len(added) != 0 {
		t.Fatal("audit overlay accounting incomplete")
	}
	return report
}
