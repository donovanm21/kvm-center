package nativevalidation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactObservedNormalizationAndRefusals(t *testing.T) {
	data, err := os.ReadFile("testdata/normalized-inactive.xml")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	for _, s := range []string{XML(testID), raw, strings.ReplaceAll(raw, "><", ">\n  <"), strings.Replace(raw, `type="mouse" bus="ps2"`, `bus="ps2" type="mouse"`, 1)} {
		if err := owned(s, testID); err != nil {
			t.Fatal(err)
		}
	}
	// Mutation variants use uniform quoting; the independently captured fixture
	// above checks observed libvirt formatting and normalization separately.
	raw = normalizedXML(testID)
	replacements := [][2]string{
		{">" + testID.Name + "<", "> " + testID.Name + " <"},
		{">" + testID.UUID + "<", "> " + testID.UUID + " <"},
		{">" + testID.Owner + "<", "> " + testID.Owner + " <"},
		{">/usr/libexec/qemu-kvm<", "> /usr/libexec/qemu-kvm <"},
		{testID.UUID, "22222222-2222-4222-8222-222222222222"}, {testID.Name, "kvmc-validation-other"}, {testID.Owner, strings.Repeat("b", 64)},
		{"262144", "524288"}, {">1</vcpu>", ">2</vcpu>"}, {`unit="KiB"`, `unit="MiB"`},
		{`type="mouse" bus="ps2"`, `type="mouse" bus="usb"`}, {`type="keyboard" bus="ps2"`, `type="keyboard" bus="virtio"`},
		{`<audio id="1" type="none"/>`, `<audio id="1" type="alsa"/>`}, {`<audio id="1" type="none"/>`, `<audio id="2" type="none"/>`},
		{`<input type="mouse" bus="ps2"/>`, `<input type="mouse" bus="ps2"><source evdev="/dev/input/event0"/></input>`},
		{`<memballoon model="none"/>`, `<memballoon model="virtio"/>`}, {`model="pci-root"`, `model="pcie-root"`},
		{`/usr/libexec/qemu-kvm`, `/tmp/other-emulator`}, {`pc-i440fx-rhel7.6.0`, `other-machine`},
		{`<devices>`, `<devices><disk type="file"><source file="/tmp/other"/></disk>`},
		{`<devices>`, `<devices><interface type="network"/>`}, {`<devices>`, `<devices><hostdev/>`},
		{`<devices>`, `<devices><filesystem/>`}, {`<devices>`, `<devices><graphics/>`},
		{`<devices>`, `<devices><input type="mouse" bus="ps2"/>`},
		{`<os>`, `<os><nvram>/tmp/other</nvram>`}, {`<os>`, `<os><loader>/tmp/other</loader>`},
		{`<os>`, `<os><kernel>/tmp/other</kernel>`}, {`<devices>`, `<devices><audio id="1" type="none"><input/></audio>`},
		{`<domain type="kvm">`, `<domain type="kvm"><memoryBacking><source type="file"/></memoryBacking>`},
		{`<domain type="kvm">`, `<domain type="kvm" xmlns:q="urn:other"><q:commandline/>`},
		{`<domain type="kvm">`, `<domain type="kvm" type="kvm">`},
		{`<name>`, `<name xmlns="urn:other">`}, {`<vcpu placement="static">`, `<vcpu placement="static" cpuset="0-7">`},
		{`<metadata>`, `<metadata><ownership xmlns="urn:kvm-center:validation:1">other</ownership>`},
	}
	for _, r := range replacements {
		changed := strings.Replace(raw, r[0], r[1], 1)
		if changed == raw {
			t.Fatalf("ineffective fixture %s", r[0])
		}
		if err := owned(changed, testID); err == nil {
			t.Fatalf("accepted unexpected profile: %s", r[1])
		}
	}
	for _, bad := range []string{raw + raw, `<!DOCTYPE domain>` + raw, raw + "trailing"} {
		if owned(bad, testID) == nil {
			t.Fatal("accepted malformed profile")
		}
	}
}

func TestStoppedRecoveryPinsInterruptedDefinition(t *testing.T) {
	d := &fakeDriver{raw: normalizedXML(testID), state: "shut off"}
	j := Journal{Identity: testID, Pending: true}
	saves := 0
	a, err := Recover(d, j, func(next Journal) error {
		saves++
		if saves == 1 && (next.Fingerprint != hash(d.raw) || next.Pending || next.Clean || len(d.calls) != 0) {
			t.Fatal("fingerprint not saved before mutation")
		}
		j = next
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.CleanupStopped(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.calls, ",") != "undefine" || !j.Clean || saves != 2 {
		t.Fatal("unexpected recovery operations", d.calls, j, saves)
	}
}

func TestStoppedRecoveryRefusesChangesAndSaveFailure(t *testing.T) {
	for _, mode := range []string{"running", "state-change", "XML-change", "save-failure", "requests", "clean-existing", "missing-pending", "wrong-owner"} {
		t.Run(mode, func(t *testing.T) {
			d := &fakeDriver{raw: normalizedXML(testID), state: "shut off"}
			j := Journal{Identity: testID, Pending: true}
			switch mode {
			case "running":
				d.state = "running"
			case "requests":
				j.Requests = []RequestRecord{{ID: "prior"}}
			case "clean-existing":
				j.Clean = true
			case "missing-pending":
				j.Pending = false
			case "wrong-owner":
				d.raw = strings.Replace(d.raw, testID.Owner, strings.Repeat("c", 64), 1)
			}
			a, err := Recover(d, j, func(Journal) error {
				switch mode {
				case "state-change":
					d.state = "running"
				case "XML-change":
					d.raw = strings.Replace(d.raw, `type="none"`, `type="alsa"`, 1)
				case "save-failure":
					return errors.New("synthetic save failure")
				}
				return nil
			})
			if err == nil {
				err = a.CleanupStopped(context.Background())
			}
			if err == nil || len(d.calls) != 0 || d.raw == "" {
				t.Fatal("unsafe recovery", err, d.calls)
			}
		})
	}
}

func TestStoppedRecoveryRuntimeDoesNotListenOrRequireSecret(t *testing.T) {
	c := config(t)
	c.CleanupOnly = true
	c.CleanupStopped = true
	c.SecretFile = "/nonexistent/synthetic-secret"
	j := Journal{Identity: testID, Pending: true}
	if err := saveJournal(c.StateDir, j); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{raw: normalizedXML(testID), state: "shut off"}
	if err := Run(context.Background(), c, d, func(string) { t.Fatal("recovery opened listener") }); err != nil {
		t.Fatal(err)
	}
	raw, err := ReadPrivate(filepath.Join(c.StateDir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &j); err != nil || !j.Clean || j.Pending || j.Fingerprint == "" {
		t.Fatal("cleanup journal not durable", err)
	}
	if strings.Join(d.calls, ",") != "undefine" {
		t.Fatal(d.calls)
	}
}

func TestStoppedRecoveryRetriesDurablePinAfterSaveFailure(t *testing.T) {
	d := &fakeDriver{raw: normalizedXML(testID), state: "shut off"}
	committed := Journal{Identity: testID, Pending: true}
	attempts := 0
	a, err := Recover(d, committed, func(next Journal) error {
		attempts++
		if attempts == 1 {
			return errors.New("synthetic persistence failure")
		}
		if attempts == 2 && (len(d.calls) != 0 || next.Fingerprint == "" || next.Pending) {
			t.Fatal("removal preceded successful durable pin")
		}
		committed = next
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.CleanupStopped(context.Background()) == nil || len(d.calls) != 0 || a.journal.Fingerprint != "" || !a.journal.Pending {
		t.Fatal("failed pin published in-memory journal")
	}
	if err = a.CleanupStopped(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || !committed.Clean || strings.Join(d.calls, ",") != "undefine" {
		t.Fatal("retry failed durable ordering", attempts, d.calls)
	}
}
