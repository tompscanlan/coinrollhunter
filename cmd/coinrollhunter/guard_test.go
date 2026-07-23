package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/api"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// om-6ex5. Loopback binding is not an authentication boundary — it only means the
// attacker has to be a webpage instead of a stranger on the internet.
//
// The attack is a CORS-*simple* POST: a JSON body sent as Content-Type: text/plain.
// decode() (internal/api) never inspects Content-Type, so Go parses it happily, and
// text/plain is CORS-safelisted, so the browser sends it with NO preflight to refuse.
// Any page the user has open can therefore fire it at http://127.0.0.1:8787 — killing
// the app (/api/quit) or, worse, silently selling a lot out of a financial ledger with
// no undo. The response is unreadable cross-origin, but the WRITE lands.
//
// The guard has to wrap the OUTER mux, because /api/quit is registered here in cmd/,
// not in api.Handler — a guard inside the API would miss the one route this is named
// for. That is why this test drives appHandler: the exact handler serveStore serves.
func TestCrossOriginSimplePostCannotQuitOrSell(t *testing.T) {
	s, lotID := storeWithALot(t)

	quit := make(chan struct{})
	var once sync.Once
	h := appHandler(s, func() { once.Do(func() { close(quit) }) }, serveOpts{})

	// The sale a hostile page would fire blind: ids are dense integer rowids, so it
	// just loops 1..500. Attacker-chosen proceeds fabricate the realized P&L.
	sale := `{"qty":1,"proceeds_usd":9999,"date":"2026-07-14"}`

	// 1. /api/quit — the nuisance payload.
	rec := attack(h, "POST", "/api/quit", "http://evil.example", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST /api/quit → %d, want 403", rec.Code)
	}
	select {
	case <-quit:
		t.Fatal("a cross-origin POST shut the app down — the quit channel was closed")
	default:
	}

	// 2. /api/lots/{id}/sell — the destructive one.
	rec = attack(h, "POST", fmt.Sprintf("/api/lots/%d/sell", lotID), "http://evil.example", sale)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST /api/lots/%d/sell → %d, want 403", lotID, rec.Code)
	}
	if got := holding(t, s, lotID); got.Disposed != "" {
		t.Errorf("the lot was SOLD by a cross-origin request (disposed=%q, disposed_usd=%v)", got.Disposed, got.DisposedUSD)
	}

	// 2b. The userinfo dodge, end-to-end through the shipped handler: "localhost" is the
	// USERNAME, evil.example is the host. It must NOT sell the lot. This pins the bypass
	// closed against a future "simplify" of the origin check to a substring match.
	rec = attack(h, "POST", fmt.Sprintf("/api/lots/%d/sell", lotID), "http://localhost@evil.example", sale)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userinfo-disguised cross-origin sell → %d, want 403", rec.Code)
	}
	if got := holding(t, s, lotID); got.Disposed != "" {
		t.Errorf("a userinfo-disguised origin (localhost@evil.example) SOLD the lot (disposed=%q)", got.Disposed)
	}

	// The server is still serving — the point of the quit check is the process, not
	// the status code.
	if rec := attack(h, "GET", "/api/health", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("after the rejected attack the server stopped answering: GET /api/health → %d", rec.Code)
	}

	// The payload itself is genuinely live: the SAME body and Content-Type, from a
	// caller the guard allows, does sell the lot. So it is the guard that stopped the
	// attack, not a malformed request.
	rec = attack(h, "POST", fmt.Sprintf("/api/lots/%d/sell", lotID), "http://127.0.0.1:8787", sale)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same-origin sell → %d, want 204 (if this fails the attack above proved nothing)", rec.Code)
	}
	if got := holding(t, s, lotID); got.Disposed == "" {
		t.Error("same-origin sell did not record the disposal")
	}

	// And quit still quits, from the app's own page.
	if rec := attack(h, "POST", "/api/quit", "http://127.0.0.1:8787", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("same-origin POST /api/quit → %d, want 204 — the Quit button must still work", rec.Code)
	}
	select {
	case <-quit:
	default:
		t.Fatal("same-origin POST /api/quit did not stop the app")
	}
}

// The qa/ suite is the release gate and it does NOT drive the API only through the
// page: qa/run.sh curls POST /api/spot, do-tab.e2e.mjs fetches from Node, and
// instanceAt() is a plain Go GET. None of them send an Origin. A guard that requires
// one turns the gate red — and the tempting "fix" is to edit the suite. So: a missing
// Origin is ALLOWED, and that is pinned here rather than left to the e2e run to
// discover. It costs nothing: a browser cannot suppress Origin on a cross-origin
// request.
func TestRequestsWithNoOriginStillWork(t *testing.T) {
	s, lotID := storeWithALot(t)
	quit := make(chan struct{})
	var once sync.Once
	h := appHandler(s, func() { once.Do(func() { close(quit) }) }, serveOpts{})

	// qa/run.sh:44 — seed the spot price with curl.
	rec := attack(h, "POST", "/api/spot", "", `{"as_of":"2026-01-01","gold_usd":4000,"silver_usd":60,"platinum_usd":1000,"palladium_usd":1100,"source":"qa"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("curl-shaped POST /api/spot (no Origin) → %d, want 200: %s", rec.Code, rec.Body)
	}
	// do-tab.e2e.mjs:498 — seed lots from Node.
	if rec := attack(h, "POST", "/api/lots", "", `{"item_type_id":1,"activity":"bullion","qty":1,"basis_usd":10,"acquired":"2026-07-01"}`); rec.Code != http.StatusCreated {
		t.Errorf("node-shaped POST /api/lots (no Origin) → %d, want 201: %s", rec.Code, rec.Body)
	}
	// launch.go instanceAt() — the single-instance health probe.
	if rec := attack(h, "GET", "/api/health", "", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /api/health (no Origin) → %d, want 200", rec.Code)
	}
	// do-tab.e2e.mjs:24 — a PUT merge from Node.
	if rec := attack(h, "PUT", fmt.Sprintf("/api/lots/%d", lotID), "", `{"notes":"seeded"}`); rec.Code != http.StatusOK {
		t.Errorf("node-shaped PUT /api/lots/{id} (no Origin) → %d, want 200: %s", rec.Code, rec.Body)
	}
	// And the Quit button's own route, called without a browser (a user's curl).
	if rec := attack(h, "POST", "/api/quit", "", ""); rec.Code != http.StatusNoContent {
		t.Errorf("POST /api/quit (no Origin) → %d, want 204", rec.Code)
	}
}

// The dev loop: `npm run dev` proxies /api to the running binary, and the Vite proxy is
// the string shorthand (vite.config.ts) — no changeOrigin — so it forwards
// Origin: http://localhost:5173 and a Host on :5173 too. A guard that pins the bound
// port kills the dev server. Loopback hostname, any port.
func TestViteDevProxyIsAllowed(t *testing.T) {
	s, _ := storeWithALot(t)
	h := appHandler(s, func() {}, serveOpts{})

	req := httptest.NewRequest("POST", "/api/spot", strings.NewReader(`{"as_of":"2026-02-02","gold_usd":4100,"silver_usd":61}`))
	req.Host = "localhost:5173"
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.ClientContractHeader, api.ClientContractVersion)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("the Vite dev proxy's request → %d, want 200: %s", rec.Code, rec.Body)
	}
}

// A tab that was already open during an upgrade keeps running the old JavaScript. Its
// Origin and Host are both honestly same-origin, so the loopback guard must not be
// mistaken for version protection.
//
// The structural defenses do not reach this case, and it is worth being precise about
// why. Merging a PUT onto the stored row protects a field the client OMITS — but the
// stale client NAMES it: the old Holdings grid hardcoded an empty notes into every edit,
// and naming a field is exactly how a legitimate clear is expressed, so the server cannot
// tell a stale blank from an intentional one. DisallowUnknownFields does not help either,
// because the field still exists. What is left is a payload that parses, validates, and
// means something the current client would never send — which is why the version is
// bumped by human judgement rather than derived from the build.
func TestStaleSameOriginBrowserCannotWriteAfterUpgrade(t *testing.T) {
	s, lotID := storeWithALot(t)
	if _, err := s.DB().Exec(`UPDATE lots SET notes = 'keep me' WHERE id = ?`, lotID); err != nil {
		t.Fatal(err)
	}
	h := appHandler(s, func() {}, serveOpts{})

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/lots/%d", lotID), strings.NewReader(`{"notes":""}`))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("stale same-origin PUT → %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := holding(t, s, lotID).Notes; got != "keep me" {
		t.Errorf("stale client cleared notes: got %q", got)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "refresh") {
		t.Errorf("409 does not tell the user how to recover: %s", rec.Body)
	}

	// A current-contract browser can still explicitly clear the field. This is the
	// server-side contract check, not a claim that today's grid sends this payload.
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/lots/%d", lotID), strings.NewReader(`{"notes":""}`))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.ClientContractHeader, api.ClientContractVersion)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("current same-origin PUT → %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := holding(t, s, lotID).Notes; got != "" {
		t.Errorf("current client did not clear notes: got %q", got)
	}
}

// DNS rebinding is the only path to actually READING the ledger (GET responses carry no
// CORS header, so a plain hostile page can fire them but cannot see the answer). It
// arrives as a request whose Host is the attacker's name — and with no Origin, because
// by then the page believes it is same-origin with us. Hence the Host check, and hence
// it applying to GET.
func TestReboundHostIsRejectedEvenOnGET(t *testing.T) {
	s, _ := storeWithALot(t)
	h := appHandler(s, func() {}, serveOpts{})

	for _, path := range []string{"/api/lots", "/api/summary", "/api/export"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "coins.evil.example:8787" // rebound to 127.0.0.1, but the Host gives it away
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with a rebound Host → %d, want 403", path, rec.Code)
		}
	}
}

// A non-loopback --addr serves the ENTIRE unauthenticated API — the whole ledger, read
// and write — to everyone who can reach the port. There is no LAN use case in this repo,
// so it is refused; the escape hatch exists for someone who knows what they are doing and
// is putting their own proxy in front of it.
func TestNonLoopbackAddrIsRefusedWithoutTheFlag(t *testing.T) {
	tests := []struct {
		addr   string
		unsafe bool
		ok     bool
	}{
		{"127.0.0.1:8787", false, true},
		{"localhost:8787", false, true},
		{"[::1]:8787", false, true},
		{"127.0.0.1:0", false, true}, // the ephemeral-port fallback
		{"0.0.0.0:8787", false, false},
		{":8787", false, false}, // a wildcard by omission is still a wildcard
		{"[::]:8787", false, false},
		{"192.168.1.5:8787", false, false},
		{"0.0.0.0:8787", true, true}, // …with the flag, it binds
		{":8787", true, true},
		{"192.168.1.5:8787", true, true},
		{"127.0.0.1:8787", true, true}, // the flag does not break the normal case
	}
	for _, tc := range tests {
		name := tc.addr
		if tc.unsafe {
			name += " --unsafe-network"
		}
		t.Run(name, func(t *testing.T) {
			err := checkAddr(tc.addr, tc.unsafe)
			if tc.ok && err != nil {
				t.Fatalf("checkAddr(%q, %v) = %v, want nil", tc.addr, tc.unsafe, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("checkAddr(%q, false) = nil — a non-loopback bind was accepted silently", tc.addr)
				}
				// The refusal has to say WHY, and how to override it — a bare
				// "invalid address" leaves the user with nowhere to go.
				msg := err.Error()
				if !strings.Contains(msg, "--unsafe-network") {
					t.Errorf("refusal %q does not name --unsafe-network", msg)
				}
				if !strings.Contains(strings.ToLower(msg), "anyone") && !strings.Contains(strings.ToLower(msg), "network") {
					t.Errorf("refusal %q does not explain the risk", msg)
				}
			}
		})
	}
}

// The flag has to be wired to the command, not just to checkAddr: `serve --addr 0.0.0.0`
// must fail before it binds anything.
func TestServeRefusesANonLoopbackAddr(t *testing.T) {
	db := filepath.Join(t.TempDir(), "crh.db")
	err := runServe([]string{"--db", db, "--addr", "0.0.0.0:0"})
	if err == nil {
		t.Fatal("serve --addr 0.0.0.0:0 started — it must refuse without --unsafe-network")
	}
	if !strings.Contains(err.Error(), "--unsafe-network") {
		t.Errorf("serve's refusal %q does not tell the user how to override it", err)
	}
}

// With the flag, the warning is loud and says what is actually at stake — an
// unauthenticated ledger, readable and writable by anyone who can reach the port.
func TestUnsafeNetworkWarningSaysWhatIsAtStake(t *testing.T) {
	w := strings.ToLower(unsafeNetworkWarning("0.0.0.0:8787"))
	for _, want := range []string{"0.0.0.0:8787", "anyone", "no password", "read"} {
		if !strings.Contains(w, want) {
			t.Errorf("the --unsafe-network warning does not mention %q:\n%s", want, w)
		}
	}
}

// --- helpers -----------------------------------------------------------------

// attack fires one request at the outer handler. origin == "" sends NO Origin header
// (curl / Node / the Go probe). The Content-Type is always text/plain, because that is
// the attack: a JSON body under a CORS-safelisted content type never preflights.
func attack(h http.Handler, method, path, origin, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8787" // httptest defaults this to example.com
	if origin != "" {
		req.Header.Set("Origin", origin)
		req.Header.Set(api.ClientContractHeader, api.ClientContractVersion)
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func storeWithALot(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "crh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Silver Eagle", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertHolding(model.Holding{
		ItemTypeID: typeID, Activity: "bullion", Qty: 1, BasisUSD: 30, Acquired: "2026-07-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func holding(t *testing.T, s *store.Store, id int64) model.Holding {
	t.Helper()
	items, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range items {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("lot %d vanished", id)
	return model.Holding{}
}
