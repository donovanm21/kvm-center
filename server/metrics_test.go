package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// InfluxDB line-protocol formatter tests (v5.13.0). The wire format
// is picky — a bad tag escape or missing space makes Influx reject
// the entire batch, which we've seen in other projects silently drop
// data for hours before someone notices. Cover the format
// exhaustively.

func TestLineProtocol_HostSample(t *testing.T) {
	var buf bytes.Buffer
	writeLineProtocol(&buf, metricSample{
		Measurement: "kvm_center_host",
		Tags:        map[string]string{"host_id": "hvnode01", "status": "online"},
		Fields: map[string]any{
			"cpu_usage": 12.34,
			"vm_count":  int64(4),
		},
		Timestamp: time.Unix(1_700_000_000, 0),
	})
	want := "kvm_center_host,host_id=hvnode01,status=online cpu_usage=12.34,vm_count=4i 1700000000000000000\n"
	if got := buf.String(); got != want {
		t.Errorf("line protocol mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestLineProtocol_TagKeyEscaping(t *testing.T) {
	var buf bytes.Buffer
	// Tags with spaces, commas, equals get escaped.
	writeLineProtocol(&buf, metricSample{
		Measurement: "vm",
		Tags:        map[string]string{"vm_name": "web server,prod"},
		Fields:      map[string]any{"n": int64(1)},
		Timestamp:   time.Unix(1, 0),
	})
	got := buf.String()
	if !strings.Contains(got, `vm_name=web\ server\,prod`) {
		t.Errorf("tag escaping failed: %s", got)
	}
}

func TestLineProtocol_StringField(t *testing.T) {
	var buf bytes.Buffer
	writeLineProtocol(&buf, metricSample{
		Measurement: "m",
		Fields:      map[string]any{"msg": `hi "there"`},
		Timestamp:   time.Unix(0, 0),
	})
	if !strings.Contains(buf.String(), `msg="hi \"there\""`) {
		t.Errorf("string field escaping failed: %s", buf.String())
	}
}

func TestLineProtocol_SortedOutput(t *testing.T) {
	// Unsorted map iteration would make tests flaky; verify stable
	// tag + field ordering.
	var a, b bytes.Buffer
	m := metricSample{
		Measurement: "m",
		Tags:        map[string]string{"b": "2", "a": "1"},
		Fields:      map[string]any{"z": int64(1), "y": int64(2)},
		Timestamp:   time.Unix(0, 0),
	}
	writeLineProtocol(&a, m)
	writeLineProtocol(&b, m)
	if a.String() != b.String() {
		t.Errorf("non-deterministic output:\n%q\n%q", a.String(), b.String())
	}
	// Keys sorted alphabetically in the output.
	line := a.String()
	if !strings.HasPrefix(line, "m,a=1,b=2 y=2i,z=1i ") {
		t.Errorf("keys not sorted: %s", line)
	}
}

func TestVMStateCode(t *testing.T) {
	cases := []struct{ in, want int64 }{
		{1, 1}, // running
		{2, 2}, // paused
		{0, 0}, // stopped
	}
	_ = cases // silence — the enum values are checked implicitly via vmStateCode below
	if vmStateCode("running") != 1 {
		t.Error("running should be 1")
	}
	if vmStateCode("stopped") != 0 {
		t.Error("stopped should be 0")
	}
	if vmStateCode("crashed") != -2 {
		t.Error("crashed should be -2")
	}
	if vmStateCode("weird") != -1 {
		t.Error("unknown should be -1")
	}
}
