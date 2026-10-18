// Generate the per-field compatibility ledger from pinned legacy declarations
// and current declarations. Run from the repository root.
package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type field struct{ Type, BSON string }

func declarations(paths ...string) map[string]map[string]field {
	out := map[string]map[string]field{}
	for _, path := range paths {
		fs := token.NewFileSet()
		f, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			panic(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			t, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			s, ok := t.Type.(*ast.StructType)
			if !ok {
				return false
			}
			fields := map[string]field{}
			for _, f := range s.Fields.List {
				var b bytes.Buffer
				if err := printer.Fprint(&b, fs, f.Type); err != nil {
					panic(err)
				}
				tag := ""
				if f.Tag != nil {
					tag, _ = strconv.Unquote(f.Tag.Value)
				}
				for _, name := range f.Names {
					if !name.IsExported() {
						continue
					}
					bson := strings.Split(reflect.StructTag(tag).Get("bson"), ",")[0]
					if bson == "" {
						bson = strings.ToLower(name.Name)
					}
					fields[name.Name] = field{strings.ReplaceAll(b.String(), "models.", ""), bson}
				}
			}
			out[t.Name.Name] = fields
			return false
		})
	}
	return out
}
func main() {
	old := declarations("internal/legacyfixture/schema.go")
	current := declarations("internal/models/models.go", "server/store.go")
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()
	w.Write([]string{"legacy_field", "legacy_bson", "legacy_type", "current_bson", "current_type", "accounting", "behavior_or_blocker"})
	names := []string{}
	for n := range old {
		names = append(names, n)
	}
	sort.Strings(names)
	count := 0
	for _, n := range names {
		fields := []string{}
		for f := range old[n] {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, name := range fields {
			// The saved profile's later tag additions are not deployed6db8c723 fields.
			if (n == "VM" && name == "Tags") || (n == "persistData" && name == "VMTags") {
				continue
			}
			a := old[n][name]
			b, exists := current[n][name]
			status, reason := "blocked", "Current declaration absent; raw offline originals retained; active semantics unsupported"
			if exists {
				status = "declared_unverified"
				reason = "Current declaration exists; complete active semantics and unknown-field write/backup retention not verified"
				if a.Type != b.Type || a.BSON != b.BSON {
					status = "blocked"
					reason = "Declared type or BSON spelling differs; mapping must be explicitly validated"
				}
			}
			if n == "Host" || n == "VM" || n == "User" || n == "Group" || n == "Role" || n == "StoragePool" || n == "RemovedHost" {
				if exists && a.Type == b.Type && a.BSON == b.BSON {
					status = "known_write_preserved"
					reason = "Retained-identity Mongo Host/VM/User/Group/Role/StoragePool/RemovedHost known-field write preservation tested; operational behavior remains current implementation"
				} else if !exists {
					status = "opaque_preserved_behavior_blocked"
					reason = "Raw root field retained through host/VM writes; no interpretation or operational compatibility claimed"
				}
			}
			if n == "Session" && exists && a.Type == b.Type && a.BSON == b.BSON {
				status = "session_auth_recovery_bounded"
				reason = "Canonical session local login/cookie auth/renewal/logout/graceful restart/recovery; opaque retained records; explicit active deletion without resurrection; immutable originals; TTL inactive; asynchronous acknowledgement"
			}
			if n == "APIToken" && exists && a.Type == b.Type && a.BSON == b.BSON {
				status = "token_auth_recovery_bounded"
				reason = "Canonical token mint/auth/scopes/expiry/revoke/graceful restart/recovery; immutable opaque originals; bounded admission; asynchronous persistence is not crash-durable acknowledgement"
			}
			if n == "AuditEvent" && exists && a.Type == b.Type && a.BSON == b.BSON {
				status = "audit_read_append_recovery_bounded"
				reason = "Canonical audit read/filter/append/chronological retention/recovery; at most 10000 records, TTL inactive, raw originals retained; shipping and historical replay excluded"
			}
			switch n {
			case "GuestTelemetryPayload", "GuestGrowResult", "guestEntry", "HostPCIDevice", "VMNetwork", "VMDisk", "VMPCIDevice", "GuestFS", "GuestInterface":
				if exists && a.Type == b.Type && a.BSON == b.BSON {
					status = "nested_preservation_conditional"
					reason = "Host/VM/guest-report container retained when known projection unchanged; edits with opaque descendants refuse; guest TTL must remain inactive for preservation; operational behavior unverified"
				}
			}
			if a.BSON == "-" {
				status = "not_persisted_bson"
				reason = "Excluded by legacy BSON contract; no persisted-field conversion claimed"
			}
			w.Write([]string{n + "." + name, a.BSON, a.Type, b.BSON, b.Type, status, reason})
			count++
		}
	}
	if err := w.Error(); err != nil {
		panic(err)
	}
	fmt.Fprintf(os.Stderr, "Accounted for %d deployed-source declared fields; preservation is distinct from operational support.\n", count)
}
