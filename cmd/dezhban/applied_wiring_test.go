package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The three commands that change the firewall WITHOUT the run loop must each
// keep the applied record honest. internal/runner's decorator covers every
// Apply the loop makes; these bypass it — `panic` most importantly, since it is
// deliberately independent of the running service, so nothing else will ever
// clear the record it leaves behind.
//
// An AST guard rather than a behavioural test because all three demand root and
// a real firewall: the helpers themselves are covered in
// print_rules_flags_test.go, but deleting a CALL to one left the whole suite
// green, which made the fix that added them unprotected. Same technique, and
// same reason, as TestNoTestInPackageMainIsParallel.
func TestEveryDirectFirewallPathKeepsTheRecordHonest(t *testing.T) {
	// The COUNT matters, not just presence: cmdBlock applies from two branches
	// (--force and the default plan), and asserting "calls it at all" stayed
	// green when either one alone lost its call — the exact deletion this guard
	// claims to catch.
	want := []struct {
		fn     string
		callee string
		calls  int
	}{
		{"cmdBlock", "recordAppliedBestEffort", 2},
		{"cmdUnblock", "clearAppliedRecordBestEffort", 1},
		{"cmdPanic", "clearAppliedRecordBestEffort", 1},
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	fns := map[string]*ast.FuncDecl{}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			fns[fn.Name.Name] = fn
		}
	}

	for _, w := range want {
		fn, ok := fns[w.fn]
		if !ok {
			t.Errorf("%s not found in main.go — this guard would pass vacuously", w.fn)
			continue
		}
		got := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == w.callee {
				got++
			}
			return true
		})
		if got != w.calls {
			t.Errorf("%s calls %s %d time(s), want %d.\n"+
				"If you MOVED the call into a helper this guard is simply out of date — update it.\n"+
				"If you REMOVED it, that path changes the firewall without keeping the applied\n"+
				"record honest, and a surface will report a posture that is not in force.",
				w.fn, w.callee, got, w.calls)
		}
	}
}
