package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/agentauth"
	"kvm-center/internal/models"
	"kvm-center/internal/nativevalidation"
)

// This driver is entirely synthetic: no virsh, socket, SSH or host process.
// The test exercises the real control-plane API and shared signing contract,
// but intentionally does not claim production-agent/native-libvirt coverage.
type validationAPIDriver struct {
	raw, state string
	actions    []string
}

func (d *validationAPIDriver) XML(context.Context, string) (string, error) {
	if d.raw == "" {
		return "", nativevalidation.ErrAbsent
	}
	return d.raw, nil
}
func (d *validationAPIDriver) Define(_ context.Context, s string) error {
	d.raw = s
	d.state = "shut off"
	return nil
}
func (d *validationAPIDriver) State(context.Context, string) (string, error) { return d.state, nil }
func (d *validationAPIDriver) Start(context.Context, string) error {
	d.actions = append(d.actions, "START")
	d.state = "running"
	return nil
}
func (d *validationAPIDriver) Destroy(context.Context, string) error {
	d.actions = append(d.actions, "FORCESTOP")
	d.state = "shut off"
	return nil
}
func (d *validationAPIDriver) Undefine(context.Context, string) error { d.raw = ""; return nil }
func TestNativeValidationAdapterAPIStateRoundTrip(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("validation-admin", "synthetic-validation-password")
	id := nativevalidation.Identity{UUID: "11111111-1111-4111-8111-111111111111", Name: "kvmc-validation-api", Owner: strings.Repeat("a", 64)}
	secret := h.Store.GetOrCreateConsoleSecret()
	d := &validationAPIDriver{}
	a, err := nativevalidation.Prepare(context.Background(), d, id, secret, func(nativevalidation.Journal) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Cleanup(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	agent := httptest.NewServer(a.Handler(func() {}))
	defer agent.Close()
	exerciseValidationAPI(t, h, agent.URL, id, secret)
	if len(d.actions) != 2 {
		t.Fatal("native actions repeated", d.actions)
	}
}

func exerciseValidationAPI(t *testing.T, h *integrationHarness, agentURL string, id nativevalidation.Identity, secret string) {
	t.Helper()
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(agentURL, "http://"))
	port, _ := strconv.Atoi(portText)
	report := func(want models.VMState) {
		t.Helper()
		req, _ := http.NewRequest("GET", agentURL+"/validation/inventory", nil)
		auth, exp, nonce, err := agentauth.Sign(secret, id.Name, "GET", "/validation/inventory", time.Now(), 0)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(agentauth.HeaderAuth, auth)
		req.Header.Set(agentauth.HeaderExp, exp)
		req.Header.Set(agentauth.HeaderNonce, nonce)
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var p models.TelemetryPayload
		if resp.StatusCode != 200 || json.Unmarshal(raw, &p) != nil || len(p.VMs) != 1 || p.VMs[0].UUID != id.UUID || p.VMs[0].State != want {
			t.Fatal("owned observation mismatch", string(raw))
		}
		p.AgentPort = port
		if status, body := h.do("POST", "/api/telemetry", p); status != 200 {
			t.Fatal(status, string(body))
		}
		code, body := h.do("GET", "/api/vms", nil)
		var vms []models.VM
		if code != 200 || json.Unmarshal(body, &vms) != nil || len(vms) != 1 || vms[0].UUID != id.UUID || vms[0].State != want {
			t.Fatal("API/native-state disagreement", code, string(body))
		}
	}
	report(models.VMStopped)
	for i, action := range []string{"START", "FORCESTOP"} {
		req := models.APICommandRequest{RequestID: "validation-" + strconv.Itoa(i), HostID: id.Name, VMUUID: id.UUID, VMName: id.Name, Action: action}
		code, body := h.do("POST", "/api/command", req)
		var r models.CommandResponse
		if code != 200 || json.Unmarshal(body, &r) != nil || !r.Success || r.RequestID != req.RequestID {
			t.Fatal("API dispatch failed", code, string(body))
		}
		want := models.VMRunning
		if action == "FORCESTOP" {
			want = models.VMStopped
		}
		report(want)
		// Real control-plane retries must not drive the native adapter twice.
		if code, body = h.do("POST", "/api/command", req); code != 200 {
			t.Fatal(code, string(body))
		}
	}
	code, _ := h.do("POST", "/api/command", models.APICommandRequest{RequestID: "non-owned", HostID: id.Name, VMUUID: "22222222-2222-4222-8222-222222222222", VMName: "other", Action: "START"})
	if code == 200 {
		t.Fatal("non-owned mutation admitted")
	}
}
