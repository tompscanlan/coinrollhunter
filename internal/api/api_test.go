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
