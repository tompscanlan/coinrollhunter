package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The chokepoint guard, and the criterion this bead lives or dies on: validation
// that covers only the generic create route is the bug, not the fix. This walks the
// store package's AST and asserts that EVERY exported mutation on *Store calls a
// model validator before it can reach the DB. It is mechanical on purpose — a future
// writer (the photos work will add InsertPhoto in a new file) fails this test until
// it validates, which forces the new door shut rather than trusting a reviewer to
// notice. If a mutation legitimately cannot validate, add it to validateAllowlist
// with a reason; do not delete this test.

// mutationRE matches the exported store mutation method names. Deletes/List/Get/
// Merge/Resolve/Load/Backup are deliberately excluded: a delete of a bad row must
// always work, and the rest are read-only or id-only repointers.
var mutationRE = regexp.MustCompile(`^(Insert|Update|Put|Sell)[A-Z]`)

// validateAllowlist names store mutations that legitimately do NOT call a validator,
// each with the reason. Empty today. Add here (with a reason) rather than removing a
// method from the guard.
var validateAllowlist = map[string]string{}

// expectedMutations is the mutation set as of this bead — the 19 the scout enumerated
// (8 inserts + 8 updates + SellHolding + PutSpot + PutSettings). The guard asserts it
// actually saw all of them, so a broken parse can't pass the test vacuously.
var expectedMutations = []string{
	"InsertItemType", "InsertHolding", "InsertRollTxn", "InsertTrip", "InsertBranch",
	"InsertSupply", "InsertLoss", "InsertKeeper",
	"UpdateItemType", "UpdateHolding", "UpdateRollTxn", "UpdateTrip", "UpdateBranch",
	"UpdateSupply", "UpdateKeeper", "UpdateLoss",
	"SellHolding", "PutSpot", "PutSettings",
}

func TestEveryStoreMutationValidates(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse store package: %v", err)
	}

	validated := map[string]bool{} // mutation name -> calls a validator
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || fn.Body == nil {
					continue
				}
				if recvTypeName(fn.Recv) != "Store" {
					continue
				}
				name := fn.Name.Name
				if !mutationRE.MatchString(name) {
					continue
				}
				seen[name] = true
				validated[name] = bodyCallsValidator(fn.Body)
			}
		}
	}

	// Every method in the mutation set must call a validator (or be allowlisted).
	for name := range seen {
		if validated[name] {
			continue
		}
		if reason, ok := validateAllowlist[name]; ok {
			t.Logf("%s is allowlisted (no validation): %s", name, reason)
			continue
		}
		t.Errorf("store mutation %s does not call a Validate* before touching the DB — "+
			"add a model Validate() call, or add it to validateAllowlist with a reason. "+
			"Do not delete this guard.", name)
	}

	// And the parse must actually have found the whole known mutation set, so this
	// test cannot pass by silently matching nothing.
	for _, want := range expectedMutations {
		if !seen[want] {
			t.Errorf("expected store mutation %s was not found by the AST walk — "+
				"the guard is not covering it (renamed? moved? parse broken?)", want)
		}
	}
	if t.Failed() {
		got := make([]string, 0, len(seen))
		for name := range seen {
			got = append(got, name)
		}
		sort.Strings(got)
		t.Logf("mutations the guard matched: %v", got)
	}
}

// recvTypeName returns the base type name of a method receiver, unwrapping a pointer:
// both `(s *Store)` and `(s Store)` yield "Store".
func recvTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// bodyCallsValidator reports whether body contains a call to a function whose name
// starts with "Validate" — either a method call (x.Validate(), sp.Validate()), a
// package call (model.ValidateSale(...)), or a bare Validate(...). That is the shape
// every mutation uses to funnel through internal/model's rules.
func bodyCallsValidator(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if strings.HasPrefix(fun.Sel.Name, "Validate") {
				found = true
			}
		case *ast.Ident:
			if strings.HasPrefix(fun.Name, "Validate") {
				found = true
			}
		}
		return true
	})
	return found
}
