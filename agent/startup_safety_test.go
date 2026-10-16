package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// This ordering invariant must hold in the actual entrypoint, not just an
// isolated helper: no request may arrive before cached trust and durable
// command history are ready. Parsing the entrypoint avoids launching real
// listeners, telemetry or hypervisor probes in the test.
func TestMain_CommandListenerStartsAfterSafetyState(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var main *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			main = fn
		}
	}
	if main == nil {
		t.Fatal("main entrypoint missing")
	}
	calls := make(map[string]token.Pos)
	fatalOnOpenFailure := false
	ast.Inspect(main.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				calls[id.Name] = call.Pos()
			}
		}
		if branch, ok := n.(*ast.IfStmt); ok && branch.Init != nil {
			opens := false
			ast.Inspect(branch.Init, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "OpenOperationsStore" {
						opens = true
					}
				}
				return true
			})
			if opens {
				ast.Inspect(branch.Body, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fatalf" {
							fatalOnOpenFailure = true
						}
					}
					return true
				})
			}
		}
		return true
	})
	listen := calls["StartCommandServer"]
	if listen == token.NoPos {
		t.Fatal("command listener missing")
	}
	for _, name := range []string{"LoadConsumedGrantsLog", "LoadCachedConsoleSecret", "LoadCachedCommandAuthActive", "OpenOperationsStore", "reconcileBoot"} {
		if calls[name] == token.NoPos || calls[name] >= listen {
			t.Errorf("%s must finish before command listener starts", name)
		}
	}
	if !fatalOnOpenFailure {
		t.Fatal("operation-store failure must stop startup before the listener")
	}
}
