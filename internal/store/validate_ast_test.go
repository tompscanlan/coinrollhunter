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

// The chokepoint guard, and the criterion om-1czp lived or died on: validation that
// covers only the generic create route is the bug, not the fix. This walks the store
// package's AST and asserts that EVERY exported mutation calls a model validator
// before it can reach the DB. It is mechanical on purpose — a future writer (the
// photos work will add InsertPhoto in a new file) fails this test until it validates,
// which forces the new door shut rather than trusting a reviewer to notice. If a
// mutation legitimately cannot validate, add it to validateAllowlist with a reason;
// do not delete this test.
//
// Widened by om-u3el: it used to walk only *Store receivers. The transaction-bound
// writer (*Tx, store.go) carries a full twin of the insert path, and a guard that
// only knew about Store would have been BLIND to it — a second, unvalidated door into
// the ledger, exactly the hole om-1czp closed. So the walk now covers EVERY method
// receiver in the package, and mutations are keyed Receiver.Method: the next writer
// cannot escape the guard by inventing a new receiver either.

// mutationRE matches the exported store mutation method names. Deletes/List/Get/
// Merge/Resolve/Load/Backup are deliberately excluded: a delete of a bad row must
// always work, and the rest are read-only or id-only repointers.
var mutationRE = regexp.MustCompile(`^(Insert|Update|Put|Sell)[A-Z]`)

// validateAllowlist names store mutations that legitimately do NOT call a validator,
// each with the reason. Keyed Receiver.Method. Empty today. Add here (with a reason)
// rather than removing a method from the guard.
var validateAllowlist = map[string]string{}

// expectedMutations is the mutation set, keyed Receiver.Method: the 19 on *Store (8
// inserts + 8 updates + SellHolding + PutSpot + PutSettings) and the 12 on the
// transaction-bound *Tx (the 8 inserts + PutSpot + PutSettings + UpdateItemType +
// UpdateHolding). The two *Tx updates were added by om-2sl6 so the compound
// holdings-with-type workflow can find-or-create-then-update a catalog row and
// merge-update a holding inside one transaction (compound.go) — twins that validate in
// their own body, exactly as om-u3el's note prescribes. The guard asserts it actually
// saw all of them, so a broken parse can't pass the test vacuously — and so that
// dropping or renaming one is a failure, not a silent gap.
var expectedMutations = []string{
	"Store.InsertItemType", "Store.InsertHolding", "Store.InsertRollTxn", "Store.InsertTrip",
	"Store.InsertBranch", "Store.InsertSupply", "Store.InsertLoss", "Store.InsertKeeper",
	"Store.UpdateItemType", "Store.UpdateHolding", "Store.UpdateRollTxn", "Store.UpdateTrip",
	"Store.UpdateBranch", "Store.UpdateSupply", "Store.UpdateKeeper", "Store.UpdateLoss",
	"Store.SellHolding", "Store.PutSpot", "Store.PutSettings",

	"Tx.InsertItemType", "Tx.InsertHolding", "Tx.InsertRollTxn", "Tx.InsertTrip",
	"Tx.InsertBranch", "Tx.InsertSupply", "Tx.InsertLoss", "Tx.InsertKeeper",
	"Tx.PutSpot", "Tx.PutSettings", "Tx.UpdateItemType", "Tx.UpdateHolding",

	// Photos (om-6hlp): the insert, the role/seq/caption update, and the soft-delete,
	// each with a *Tx twin — every one validates in its own body (photos.go).
	"Store.InsertPhoto", "Store.UpdatePhoto", "Store.UpdatePhotoInactive",
	"Tx.InsertPhoto", "Tx.UpdatePhoto", "Tx.UpdatePhotoInactive",
}

func TestEveryStoreMutationValidates(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse store package: %v", err)
	}

	validated := map[string]bool{} // Receiver.Method -> calls a validator
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || fn.Body == nil {
					continue
				}
				// Deliberately NOT filtered to one receiver type: a mutation on any
				// receiver is a door into the ledger and must validate.
				recv := recvTypeName(fn.Recv)
				if recv == "" || !mutationRE.MatchString(fn.Name.Name) {
					continue
				}
				name := recv + "." + fn.Name.Name
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

	// A new mutation on a new receiver is a new door into the ledger. It is caught by
	// the validation check above, but it must also be DECLARED here, so that adding one
	// is a deliberate act with a test edit behind it rather than a quiet widening of the
	// write surface.
	want := map[string]bool{}
	for _, name := range expectedMutations {
		want[name] = true
	}
	for name := range seen {
		if !want[name] {
			t.Errorf("undeclared store mutation %s — a new writer appeared. Add it to "+
				"expectedMutations (and make sure it validates).", name)
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
