package api_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/api"
	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/legacy"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "sample-data")
	holdings, _ := os.ReadFile(filepath.Join(root, "pm_holdings.sample.json"))
	crh, _ := os.ReadFile(filepath.Join(root, "crh_ledger.sample.json"))
	if err := legacy.Import(s, holdings, crh); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler(s, nil))
	t.Cleanup(func() { srv.Close(); s.Close() })
	return srv
}

func TestSummaryEndpoint(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/api/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var r calc.Report
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.CRHNetReal-50.10896) > 1e-6 {
		t.Errorf("crh_net_real = %.5f, want 50.10896", r.CRHNetReal)
	}
	if r.Verdict() != "PROFITABLE (cash basis)" {
		t.Errorf("verdict = %q", r.Verdict())
	}
}

func TestLotsCRUD(t *testing.T) {
	srv := newServer(t)

	// List starts at 4 (the imported sample holdings).
	if n := countLots(t, srv.URL); n != 4 {
		t.Fatalf("initial lots = %d, want 4", n)
	}

	// Create needs an item_type to point at; reuse id 1 from the import.
	body, _ := json.Marshal(model.Holding{ItemTypeID: 1, Activity: "bullion", Qty: 2, BasisUSD: 99.50, Acquired: "2026-02-02"})
	resp, err := http.Post(srv.URL+"/api/lots", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == 0 {
		t.Fatal("created id is 0")
	}
	if n := countLots(t, srv.URL); n != 5 {
		t.Fatalf("after create lots = %d, want 5", n)
	}

	// Delete it; back to 4.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/lots/"+strconv.FormatInt(created.ID, 10), nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	if n := countLots(t, srv.URL); n != 4 {
		t.Fatalf("after delete lots = %d, want 4", n)
	}

	// Deleting a missing id is a 404.
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/lots/99999", nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete missing status = %d, want 404", resp.StatusCode)
	}
}

func countLots(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Get(base + "/api/lots")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lots []model.Holding
	if err := json.NewDecoder(resp.Body).Decode(&lots); err != nil {
		t.Fatal(err)
	}
	return len(lots)
}

// The download the UI's "Export my data" button pulls: a real zip, named for the day
// it was made, with the whole bundle inside. The bundle's own contents are the
// export package's tests to keep; this asserts the HTTP contract around them.
func TestExportEndpointServesAZipBundle(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/api/export")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q — the browser would render it, not save it", cd)
	}
	if !strings.Contains(cd, "coinrollhunter-export-"+time.Now().Format("2006-01-02")+".zip") {
		t.Errorf("Content-Disposition = %q, want today's dated bundle name", cd)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the download is not a readable zip: %v", err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	for _, want := range []string{
		"item_type.csv", "lots.csv", "roll_txns.csv", "keepers.csv", "trips.csv", "supplies.csv",
		"losses.csv", "branches.csv", "branch_aliases.csv", "spot.csv", "settings.csv", "photos.csv",
		"data.json", "manifest.json", "photos/",
	} {
		if !got[want] {
			t.Errorf("the downloaded bundle is missing %s", want)
		}
	}
}

// The API export path serves a real file-backed store directly (no throwaway copy — that
// is only the CLI's concern, since the running app already holds the migrated DB). Two
// things to hold: it carries the photos that live beside that database, and it does not
// mutate the file it reads.
func TestExportEndpointCarriesPhotosAndDoesNotMutateTheDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "crh.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	const owner, photo = "owner-1", "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES (?,?,?,?,0,?)`,
		photo, "lot", owner, "obverse", "jpg"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "photos", owner), 0o755); err != nil {
		t.Fatal(err)
	}
	pic := []byte("\xff\xd8 api-served coin")
	if err := os.WriteFile(filepath.Join(dir, "photos", owner, photo+".jpg"), pic, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.Handler(s, nil))
	t.Cleanup(func() { srv.Close(); s.Close() })

	before := dbSHA(t, dbPath)
	resp, err := http.Get(srv.URL + "/api/export")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if after := dbSHA(t, dbPath); after != before {
		t.Error("the API export mutated the database file it read — export must be read-only")
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a readable zip: %v", err)
	}
	var served []byte
	for _, f := range zr.File {
		if f.Name == "photos/"+owner+"/"+photo+".jpg" {
			rc, _ := f.Open()
			served, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	if string(served) != string(pic) {
		t.Errorf("the API export did not carry the photo beside the DB (got %d bytes)", len(served))
	}
}

func dbSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PUT is a merge, not a replace. This is the pin for the shipped data-loss bug: the
// Holdings grid models only some of a lot's columns, and a full-replace PUT rewrote
// the whole row from that partial view — so editing a quantity blanked the notes a
// user's spreadsheet import had just filled in. No warning, no undo, and the field is
// not even on screen, so nobody finds out until they go looking.
//
// The body below is deliberately shaped like the real client's: exactly the columns
// the grid edits, and not one more.
func TestPutMergeKeepsColumnsTheClientNeverNamed(t *testing.T) {
	srv := newServer(t)

	// A lot with the fields no grid shows: notes (the import writes these), plus
	// insured_value and attributes (latent today; the estate sheet surfaces them).
	full := model.Holding{
		ItemTypeID: 1, Activity: "bullion", Qty: 2, BasisUSD: 99.50, Acquired: "2026-02-02",
		Source: "LCS", Location: "home safe", Notes: "grandfather's, do not sell",
		InsuredValue: 1234.56, Attributes: `{"grade":"MS65"}`,
	}
	id := createLot(t, srv.URL, full)

	// The gesture: bump the quantity in the grid. The client sends only what it models.
	putLot(t, srv.URL, id, map[string]any{
		"item_type_id": 1, "activity": "bullion", "qty": 3, "basis_usd": 99.50,
		"acquired": "2026-02-02", "source": "LCS", "location": "home safe",
		"face_value_usd": 0, "premium_usd": 0,
	}, http.StatusOK)

	got := getLot(t, srv.URL, id)
	if got.Qty != 3 {
		t.Errorf("qty = %v, want 3 (the edit must land)", got.Qty)
	}
	if got.Notes != full.Notes {
		t.Errorf("notes = %q, want %q — a quantity edit destroyed the user's notes", got.Notes, full.Notes)
	}
	if got.InsuredValue != full.InsuredValue {
		t.Errorf("insured_value = %v, want %v", got.InsuredValue, full.InsuredValue)
	}
	if got.Attributes != full.Attributes {
		t.Errorf("attributes = %q, want %q", got.Attributes, full.Attributes)
	}
}

// The other half of the merge contract: a field you DO name still changes, including
// to its zero value. Preserving what the client omits must not turn into refusing to
// clear what it explicitly sends — that would make notes uneditable rather than safe.
func TestPutStillClearsAFieldTheClientNames(t *testing.T) {
	srv := newServer(t)
	id := createLot(t, srv.URL, model.Holding{
		ItemTypeID: 1, Activity: "bullion", Qty: 1, Acquired: "2026-02-02",
		Notes: "sold at the show", InsuredValue: 500,
	})

	putLot(t, srv.URL, id, map[string]any{"notes": "", "insured_value": 0}, http.StatusOK)

	got := getLot(t, srv.URL, id)
	if got.Notes != "" {
		t.Errorf("notes = %q, want \"\" — an explicit clear must still clear", got.Notes)
	}
	if got.InsuredValue != 0 {
		t.Errorf("insured_value = %v, want 0", got.InsuredValue)
	}
}

// The acceptance criterion this bug earned the hard way: the om-yhbr e2e reproduced it
// by editing a lot the same suite had already sold, which resurrected the sale — and
// the suite still reported PASS. Disposed lots sit unmarked in the Holdings grid, so
// this is an ordinary thing for a user to do by accident.
func TestPutOnASoldLotDoesNotResurrectIt(t *testing.T) {
	srv := newServer(t)
	id := createLot(t, srv.URL, model.Holding{
		ItemTypeID: 1, Activity: "bullion", Qty: 1, BasisUSD: 100, Acquired: "2026-02-02",
	})

	sell, _ := json.Marshal(map[string]any{"qty": 1, "proceeds_usd": 175, "date": "2026-03-03"})
	resp, err := http.Post(srv.URL+"/api/lots/"+strconv.FormatInt(id, 10)+"/sell", "application/json", bytes.NewReader(sell))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sell status %d", resp.StatusCode)
	}

	// Now touch any unrelated cell on that row through the grid's payload shape.
	putLot(t, srv.URL, id, map[string]any{
		"item_type_id": 1, "activity": "bullion", "qty": 1, "basis_usd": 100,
		"acquired": "2026-02-02", "source": "estate sale",
	}, http.StatusOK)

	got := getLot(t, srv.URL, id)
	if got.Source != "estate sale" {
		t.Errorf("source = %q, want %q (the edit must land)", got.Source, "estate sale")
	}
	if got.Disposed != "2026-03-03" {
		t.Errorf("disposed = %q, want %q — an unrelated cell edit un-sold the lot", got.Disposed, "2026-03-03")
	}
	if got.DisposedUSD != 175 {
		t.Errorf("disposed_usd = %v, want 175 — the sale's proceeds were erased", got.DisposedUSD)
	}
}

// A PUT at an id that does not exist is a 404, not a silent no-op: the merge has no
// row to merge onto.
func TestPutMissingIDIs404(t *testing.T) {
	srv := newServer(t)
	putLot(t, srv.URL, 99999, map[string]any{"qty": 1}, http.StatusNotFound)
}

// The bead's own framing: the API is "not an integrity boundary against a direct
// curl." A POST with a body full of impossible values must come back 400 with a
// message that names the offending field — not a 500, and not a 201 that silently
// poisons the ledger — and it must not create a row. This is the create half of the
// hole the review found (decode -> store with nothing between).
func TestValidationRejectsBadPOST(t *testing.T) {
	srv := newServer(t)

	before := countLots(t, srv.URL)

	// The exact shape from the dispatch contract's curl proof.
	body := []byte(`{"item_type_id":1,"activity":"crh","qty":-5,"basis_usd":-1,"purity":9.9,"acquired":"nope"}`)
	resp, err := http.Post(srv.URL+"/api/lots", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST bad lot status = %d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	if e.Error == "" {
		t.Fatal("400 body carried no error message")
	}
	// The message names a field so the client can point at the cell (any one of the
	// invalid fields is fine — validation stops at the first).
	named := false
	for _, f := range []string{"qty", "basis_usd", "purity", "acquired", "activity"} {
		if strings.Contains(e.Error, f) {
			named = true
		}
	}
	if !named {
		t.Errorf("error %q names none of the offending fields", e.Error)
	}
	if after := countLots(t, srv.URL); after != before {
		t.Errorf("a rejected POST created a lot: %d -> %d", before, after)
	}

	// A bad enum on another resource returns 400 too, not 500.
	rt := []byte(`{"action":"sell","date":"2026-01-01","face_usd":10}`)
	rresp, err := http.Post(srv.URL+"/api/roll-txns", "application/json", bytes.NewReader(rt))
	if err != nil {
		t.Fatal(err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST bad roll-txn status = %d, want 400", rresp.StatusCode)
	}
	var re struct {
		Error string `json:"error"`
	}
	json.NewDecoder(rresp.Body).Decode(&re)
	if !strings.Contains(re.Error, "action") {
		t.Errorf("roll-txn error %q does not name the action field", re.Error)
	}
}

// The update door the review missed: PUT is a merge, so a curl can poison an
// existing row exactly as easily as a new one. A PUT that merges an invalid value
// onto a stored row is a 400 with a field-named message, and the stored row is left
// unchanged.
func TestValidationRejectsBadPUT(t *testing.T) {
	srv := newServer(t)
	id := createLot(t, srv.URL, model.Holding{
		ItemTypeID: 1, Activity: "bullion", Qty: 2, BasisUSD: 100, Acquired: "2026-02-02",
	})

	// Merge a negative quantity onto the row (the grid's payload shape).
	b, _ := json.Marshal(map[string]any{"qty": -3})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/lots/"+strconv.FormatInt(id, 10), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT bad qty status = %d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	if !strings.Contains(e.Error, "qty") {
		t.Errorf("error %q does not name the qty field", e.Error)
	}

	// The stored row must be untouched — the rejected merge wrote nothing.
	got := getLot(t, srv.URL, id)
	if got.Qty != 2 {
		t.Errorf("qty = %v, want 2 — a rejected PUT mutated the row", got.Qty)
	}
}

func createLot(t *testing.T, base string, h model.Holding) int64 {
	t.Helper()
	body, _ := json.Marshal(h)
	resp, err := http.Post(base+"/api/lots", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create lot status %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	return created.ID
}

// putLot sends a raw map, not a model.Holding: the point of every test above is which
// keys are on the wire, and marshalling a struct would either add keys the client
// never sends or drop them to omitempty.
func putLot(t *testing.T, base string, id int64, body map[string]any, wantStatus int) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, base+"/api/lots/"+strconv.FormatInt(id, 10), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("put status %d, want %d", resp.StatusCode, wantStatus)
	}
}

func getLot(t *testing.T, base string, id int64) model.Holding {
	t.Helper()
	resp, err := http.Get(base + "/api/lots")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lots []model.Holding
	if err := json.NewDecoder(resp.Body).Decode(&lots); err != nil {
		t.Fatal(err)
	}
	for _, l := range lots {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("lot %d not found", id)
	return model.Holding{}
}
