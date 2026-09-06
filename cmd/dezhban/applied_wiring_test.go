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
	want := map[string]string{
		"cmdBlock":   "recordAppliedBestEffort",
		"cmdUnblock": "clearAppliedRecordBestEffort",
		"cmdPanic":   "clearAppliedRecordBestEffort",
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	found := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		callee, watched := want[fn.Name.Name]
		if !watched {
			continue
		}
		found[fn.Name.Name] = true
		calls := false
		ast.Inspect(fn, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == callee {
				calls = true
			}
			return !calls
		})
		if !calls {
			t.Errorf("%s does not call %s — it changes the firewall directly, so the "+
				"applied record would describe a posture that is not in force", fn.Name.Name, callee)
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("%s not found in main.go — this guard would pass vacuously", name)
		}
	}
}
