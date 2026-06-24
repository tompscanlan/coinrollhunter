package api_test

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
	root := filepath.Join("..", "..", "prototype", "sample-data")
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
