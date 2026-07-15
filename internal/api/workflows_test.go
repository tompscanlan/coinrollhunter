package api_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// End-to-end atomicity proofs for the /api/workflows/* composite endpoints (om-2sl6),
// over real HTTP. Each drives an endpoint with a payload whose LAST step is invalid — a
// real, non-synthetic mid-transaction failure thanks to om-1czp's server-side
// validation — and asserts that ZERO rows were written by ANY step, counting every
// affected table before and after via the granular GET endpoints. A test that only
// checked the request returned non-2xx would NOT prove rollback; these count the rows.

func countVia(t *testing.T, base, path string) int {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var arr []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return len(arr)
}

// workflowCounts snapshots every table a compound action can touch — including branches,
// the sneaky one: a box insert forks a branch as a side effect, and a rolled-back box
// must not leave it behind (seam f).
func workflowCounts(t *testing.T, base string) map[string]int {
	t.Helper()
	m := map[string]int{}
	for _, p := range []string{"/api/lots", "/api/item-types", "/api/roll-txns", "/api/trips", "/api/keepers", "/api/branches"} {
		m[p] = countVia(t, base, p)
	}
	return m
}

func assertCountsEqual(t *testing.T, before, after map[string]int) {
	t.Helper()
	for p, b := range before {
		if after[p] != b {
			t.Errorf("%s: %d -> %d, want unchanged — a rolled-back workflow wrote a row", p, b, after[p])
		}
	}
}

func postWorkflow(t *testing.T, base, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// --- bought-a-box -----------------------------------------------------------------

func TestWorkflowBoughtABoxHappyPath(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"purchase":{"date":"2026-03-01","bank":"WF New Branch","action":"buy","denom":"halves","unit":"box","amount":1,"face_usd":500,"source_type":"customer_roll","notes":"first box"},` +
		`"trip":{"date":"2026-03-01","bank":"WF New Branch","miles":8,"hours":0.5}}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/bought-a-box", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d, want 201", resp.StatusCode)
	}
	after := workflowCounts(t, srv.URL)
	for p, want := range map[string]int{"/api/roll-txns": 1, "/api/trips": 1, "/api/branches": 1} {
		if got := after[p] - before[p]; got != want {
			t.Errorf("%s delta = %d, want %d", p, got, want)
		}
	}
}

// The seam-f proof: a valid purchase that forks a new branch, then an INVALID trip.
// The whole action rolls back — including the branch (the "sneaky" GET below).
func TestWorkflowBoughtABoxAtomicOnBadTrip(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"purchase":{"date":"2026-03-01","bank":"WF Orphan Branch","action":"buy","denom":"halves","unit":"box","amount":1,"face_usd":500,"notes":""},` +
		`"trip":{"date":"2026-03-01","bank":"WF Orphan Branch","miles":-5,"hours":0}}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/bought-a-box", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (invalid trip)", resp.StatusCode)
	}
	assertCountsEqual(t, before, workflowCounts(t, srv.URL))
}

// --- logged-finds -----------------------------------------------------------------

func TestWorkflowLoggedFindsHappyPath(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"box":{"new":{"date":"2026-03-02","bank":"WF Finds Branch","action":"buy","denom":"halves","unit":"box","amount":1,"face_usd":500,"notes":""}},` +
		`"finds":[{"product":"90% half","metal":"silver","fineness":"90%","fine_oz_each":0.36169,"qty":3,"basis_usd":1.5,"premium_usd":0,"face_value_usd":1.5,"acquired":"2026-03-02","source":"WF Finds Branch","kept":true}],` +
		`"keepers":[{"denom":"halves","count":10,"face_usd":5,"date":"2026-03-02"}]}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/logged-finds", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d, want 204", resp.StatusCode)
	}
	after := workflowCounts(t, srv.URL)
	for p, want := range map[string]int{"/api/roll-txns": 1, "/api/item-types": 1, "/api/lots": 1, "/api/keepers": 1, "/api/branches": 1} {
		if got := after[p] - before[p]; got != want {
			t.Errorf("%s delta = %d, want %d", p, got, want)
		}
	}
}

// The seam-b + seam-f proof: a new box that forks a new branch, a valid first find, then
// an INVALID second find (negative basis). The box, its branch, its item_type and the
// first lot all roll back.
func TestWorkflowLoggedFindsAtomicOnBadFind(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"box":{"new":{"date":"2026-03-02","bank":"WF Finds Orphan","action":"buy","denom":"halves","unit":"box","amount":1,"face_usd":500,"notes":""}},` +
		`"finds":[` +
		`{"product":"90% half","metal":"silver","fineness":"90%","fine_oz_each":0.36169,"qty":1,"basis_usd":0.5,"premium_usd":0,"face_value_usd":0.5,"acquired":"2026-03-02","source":"WF Finds Orphan","kept":true},` +
		`{"product":"bad find","metal":"silver","fineness":"","fine_oz_each":0,"qty":1,"basis_usd":-1,"premium_usd":0,"face_value_usd":0,"acquired":"2026-03-02","source":"","kept":false}` +
		`],"keepers":[]}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/logged-finds", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (invalid find)", resp.StatusCode)
	}
	assertCountsEqual(t, before, workflowCounts(t, srv.URL))
}

// A failing keeper (the last step) also rolls back the box, its branch and the finds.
func TestWorkflowLoggedFindsAtomicOnBadKeeper(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"box":{"new":{"date":"2026-03-02","bank":"WF Keeper Orphan","action":"buy","denom":"halves","unit":"box","amount":1,"face_usd":500,"notes":""}},` +
		`"finds":[{"product":"90% half","metal":"silver","fineness":"90%","fine_oz_each":0.36169,"qty":1,"basis_usd":0.5,"premium_usd":0,"face_value_usd":0.5,"acquired":"2026-03-02","source":"","kept":true}],` +
		`"keepers":[{"denom":"halves","count":-3,"face_usd":0,"date":"2026-03-02"}]}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/logged-finds", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (invalid keeper)", resp.StatusCode)
	}
	assertCountsEqual(t, before, workflowCounts(t, srv.URL))
}

// --- holdings-with-type -----------------------------------------------------------

func TestWorkflowHoldingsWithTypeHappyPath(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"catalog":{"product":"1 oz Platinum Eagle","metal":"platinum","fineness":".9995","fine_oz_each":1},` +
		`"holding":{"activity":"bullion","roll_txn_id":0,"qty":1,"gross_weight":0,"purity":0,"weight_unit":"ozt","basis_usd":1050,"premium_usd":0,"face_value_usd":0,"acquired":"2026-03-03","source":"APMEX","location":"","category":"","subcategory":"","trophy":false,"kept":false,"notes":""}}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/holdings-with-type", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d, want 201", resp.StatusCode)
	}
	after := workflowCounts(t, srv.URL)
	for p, want := range map[string]int{"/api/lots": 1, "/api/item-types": 1} {
		if got := after[p] - before[p]; got != want {
			t.Errorf("%s delta = %d, want %d", p, got, want)
		}
	}
}

// A bad holding rejects AFTER the item_type was find-or-created, so the new catalog row
// must roll back with it — no orphan type, no lot.
func TestWorkflowHoldingsWithTypeAtomicOnBadHolding(t *testing.T) {
	srv := newServer(t)
	before := workflowCounts(t, srv.URL)
	body := `{"catalog":{"product":"WF Orphan Type","metal":"gold","fineness":"","fine_oz_each":1},` +
		`"holding":{"activity":"hoard","roll_txn_id":0,"qty":1,"gross_weight":0,"purity":0,"weight_unit":"ozt","basis_usd":1,"premium_usd":0,"face_value_usd":0,"acquired":"2026-03-03","source":"","location":"","category":"","subcategory":"","trophy":false,"kept":false,"notes":""}}`
	resp := postWorkflow(t, srv.URL, "/api/workflows/holdings-with-type", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (bad activity)", resp.StatusCode)
	}
	assertCountsEqual(t, before, workflowCounts(t, srv.URL))
}

// The Edit-grid UPDATE is a MERGE: a column the client does not name in `holding`
// survives (the om-kyq7 guarantee, now through the composite tx). Seed notes via the
// granular PUT, then update qty through the workflow endpoint and assert notes stayed.
func TestWorkflowHoldingsWithTypeUpdateIsAMerge(t *testing.T) {
	srv := newServer(t)
	// Reuse an imported lot (item_type id 1 exists from the sample import).
	id := createLot(t, srv.URL, model.Holding{ItemTypeID: 1, Activity: "bullion", Qty: 1, BasisUSD: 100, Acquired: "2026-03-04"})
	putLot(t, srv.URL, id, map[string]any{"notes": "do not blank me", "insured_value": 4500}, http.StatusOK)

	// A workflow update that names only qty (and the catalog) — notes/insured_value are
	// NOT in the `holding` object, so the merge must preserve them.
	body := `{"catalog":{"product":"Merge Type","metal":"gold","fineness":"","fine_oz_each":1},` +
		`"holding":{"activity":"bullion","roll_txn_id":0,"qty":7,"gross_weight":0,"purity":0,"weight_unit":"","basis_usd":100,"premium_usd":0,"face_value_usd":0,"acquired":"2026-03-04","source":"","location":"","category":"","subcategory":"","trophy":false,"kept":false}}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/workflows/holdings-with-type/"+strconv.FormatInt(id, 10), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	got := getLot(t, srv.URL, id)
	if got.Qty != 7 {
		t.Errorf("qty = %v, want 7 (the named edit must land)", got.Qty)
	}
	if got.Notes != "do not blank me" {
		t.Errorf("notes = %q, want preserved (a merge must not blank an unnamed column)", got.Notes)
	}
	if got.InsuredValue != 4500 {
		t.Errorf("insured_value = %v, want 4500 preserved", got.InsuredValue)
	}
}
