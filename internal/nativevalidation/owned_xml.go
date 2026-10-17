package nativevalidation

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// normalizedXML is one observed libvirt profile, not a general device allowlist.
// Other machines, emulator paths or device attributes require separate review.
// No disk, NIC, graphics, host device, external backing or writable firmware.
func normalizedXML(id Identity) string {
	return fmt.Sprintf(`<domain type="kvm"><name>%s</name><uuid>%s</uuid><metadata><ownership xmlns="urn:kvm-center:validation:1">%s</ownership></metadata><memory unit="KiB">262144</memory><currentMemory unit="KiB">262144</currentMemory><vcpu placement="static">1</vcpu><os><type arch="x86_64" machine="pc-i440fx-rhel7.6.0">hvm</type><boot dev="hd"/></os><cpu mode="custom" match="exact" check="none"><model fallback="forbid">qemu64</model></cpu><clock offset="utc"/><on_poweroff>destroy</on_poweroff><on_reboot>destroy</on_reboot><on_crash>destroy</on_crash><devices><emulator>/usr/libexec/qemu-kvm</emulator><controller type="usb" index="0" model="none"/><controller type="pci" index="0" model="pci-root"/><input type="mouse" bus="ps2"/><input type="keyboard" bus="ps2"/><audio id="1" type="none"/><memballoon model="none"/></devices></domain>`, id.Name, id.UUID, id.Owner)
}

type xmlShape struct {
	Name     xml.Name
	Attr     []xml.Attr
	Text     string
	Children []*xmlShape
}

// Match XML structure, not formatting. Expanded namespace names remain part of
// every comparison. Reject directives, duplicate attributes and multiple roots;
// no unrecognized element/attribute can disappear through struct unmarshalling.
func shape(raw string) (string, error) {
	if len(raw) > 1<<20 {
		return "", errors.New("domain XML exceeds limit")
	}
	dec := xml.NewDecoder(strings.NewReader(raw))
	var root *xmlShape
	var stack []*xmlShape
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("invalid domain XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= 32 {
				return "", errors.New("domain XML nesting exceeds limit")
			}
			n := &xmlShape{Name: t.Name}
			seen := map[xml.Name]bool{}
			for _, a := range t.Attr {
				if seen[a.Name] {
					return "", errors.New("duplicate XML attribute")
				}
				seen[a.Name] = true
				// Namespace declarations affect expanded names; they are not device data.
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				n.Attr = append(n.Attr, a)
			}
			sort.Slice(n.Attr, func(i, j int) bool {
				a, b := n.Attr[i].Name, n.Attr[j].Name
				if a.Space != b.Space {
					return a.Space < b.Space
				}
				return a.Local < b.Local
			})
			if len(stack) == 0 {
				if root != nil {
					return "", errors.New("multiple domain roots")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return "", errors.New("unbalanced domain XML")
			}
			n := stack[len(stack)-1]
			if len(n.Children) > 0 && strings.TrimSpace(n.Text) == "" {
				n.Text = ""
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return "", errors.New("data outside domain")
				}
			} else {
				stack[len(stack)-1].Text += string(t)
			}
		case xml.Comment:
			// Comments have no guest execution semantics.
		default:
			return "", errors.New("unsupported domain XML instruction")
		}
	}
	if root == nil || len(stack) != 0 {
		return "", errors.New("incomplete domain XML")
	}
	data, err := json.Marshal(root)
	return string(data), err
}

func owned(raw string, id Identity) error {
	if err := id.Validate(); err != nil {
		return err
	}
	actual, err := shape(raw)
	if err != nil {
		return err
	}
	for _, expected := range []string{XML(id), normalizedXML(id)} {
		candidate, err := shape(expected)
		if err != nil {
			return err
		}
		if actual == candidate {
			return nil
		}
	}
	return errors.New("domain ownership or exact empty-guest configuration mismatch; refusing mutation")
}
