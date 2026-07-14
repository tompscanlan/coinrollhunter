package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The guard is the whole of the om-6ex5 fix, so its rules are pinned here one by one.
//
// The rule that works — and the one a naive implementation gets wrong — is:
//   - a MISSING Origin is ALLOWED (curl, the Go health probe, the Node e2e fetches; a
//     browser cannot suppress Origin on a cross-origin request, so this costs nothing)
//   - the Origin/Host HOSTNAME must be loopback
//   - the PORT is never pinned (the ephemeral-port fallback moves it, and the Vite dev
//     proxy forwards Origin: http://localhost:5173)
func TestGuardLoopbackOnly(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		host   string
		origin string // "" = header absent
		want   int
	}{
		// --- the ordinary app: a browser on the bound loopback address -----------
		{"same-origin GET", "GET", "/api/health", "127.0.0.1:8787", "http://127.0.0.1:8787", http.StatusOK},
		{"same-origin POST", "POST", "/api/spot", "127.0.0.1:8787", "http://127.0.0.1:8787", http.StatusOK},
		{"localhost host and origin", "GET", "/api/health", "localhost:8787", "http://localhost:8787", http.StatusOK},
		{"ipv6 loopback", "GET", "/api/health", "[::1]:8787", "http://[::1]:8787", http.StatusOK},
		{"127.0.0.2 is still loopback", "GET", "/api/health", "127.0.0.2:8787", "http://127.0.0.2:8787", http.StatusOK},

		// --- the port is NOT pinned ---------------------------------------------
		// The ephemeral-port fallback (launch.go) and `serve --addr :NNNN` both move it,
		// and qa/run.sh runs on whatever PORT it is handed.
		{"ephemeral port", "POST", "/api/spot", "127.0.0.1:41235", "http://127.0.0.1:41235", http.StatusOK},
		{"vite dev proxy: origin on :5173, host on :5173", "POST", "/api/spot", "localhost:5173", "http://localhost:5173", http.StatusOK},
		{"vite dev proxy: origin on :5173, host rewritten", "POST", "/api/spot", "127.0.0.1:8787", "http://localhost:5173", http.StatusOK},
		{"host with no port at all", "GET", "/api/health", "127.0.0.1", "", http.StatusOK},

		// --- a MISSING Origin is allowed (qa/run.sh's curl, do-tab.e2e.mjs, instanceAt) ---
		{"curl: no origin", "POST", "/api/spot", "127.0.0.1:8799", "", http.StatusOK},
		{"go health probe: no origin", "GET", "/api/health", "127.0.0.1:8787", "", http.StatusOK},
		{"no origin, no host (HTTP/1.0 client)", "GET", "/api/health", "", "", http.StatusOK},

		// --- the attack: a hostile page, cross-origin ----------------------------
		{"hostile page POSTs quit", "POST", "/api/quit", "127.0.0.1:8787", "http://evil.example", http.StatusForbidden},
		{"hostile page POSTs sell", "POST", "/api/lots/1/sell", "127.0.0.1:8787", "https://evil.example", http.StatusForbidden},
		{"hostile page reads", "GET", "/api/lots", "127.0.0.1:8787", "http://evil.example", http.StatusForbidden},
		{"sandboxed iframe / file:// origin", "POST", "/api/quit", "127.0.0.1:8787", "null", http.StatusForbidden},
		{"a suffix that only looks loopback", "POST", "/api/quit", "127.0.0.1:8787", "http://localhost.evil.example", http.StatusForbidden},
		{"an ip-shaped prefix that only looks loopback", "POST", "/api/quit", "127.0.0.1:8787", "http://127.0.0.1.evil.example", http.StatusForbidden},
		{"a public name that resolves to loopback", "POST", "/api/quit", "127.0.0.1:8787", "http://localtest.me:8787", http.StatusForbidden},

		// --- DNS rebinding: the Host is the attacker's name, and there is no Origin
		// at all because by then the page is "same-origin" with us. GETs included —
		// that is the only path to actually READING the ledger.
		{"rebound host, GET", "GET", "/api/lots", "evil.example:8787", "", http.StatusForbidden},
		{"rebound host, POST", "POST", "/api/quit", "evil.example:8787", "", http.StatusForbidden},
		{"rebound host, same-origin with itself", "GET", "/api/lots", "evil.example:8787", "http://evil.example:8787", http.StatusForbidden},
		{"a LAN address in the Host", "GET", "/api/lots", "192.168.1.5:8787", "", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, reached := serveGuarded(t, GuardOpts{}, tc.method, tc.path, tc.host, tc.origin)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (host %q, origin %q)", rec.Code, tc.want, tc.host, tc.origin)
			}
			if reached != (tc.want == http.StatusOK) {
				t.Errorf("handler reached = %v, want %v — a rejected request must never touch the API", reached, tc.want == http.StatusOK)
			}
		})
	}
}

// --unsafe-network is the escape hatch for someone who knowingly serves the LAN. It
// relaxes the HOST check to the bound address (otherwise nothing could reach the
// server it just bound) — but it does NOT open the door to a hostile webpage: a
// cross-origin request is still refused.
func TestGuardUnsafeNetwork(t *testing.T) {
	tests := []struct {
		name   string
		opts   GuardOpts
		host   string
		origin string
		want   int
	}{
		// bound to a wildcard: any Host can legitimately reach us, so the Host check
		// cannot pin anything — but the Origin check still does all the real work.
		{"wildcard bind, LAN client, no origin", GuardOpts{UnsafeNetwork: true, BoundAddr: "0.0.0.0:8787"}, "192.168.1.5:8787", "", http.StatusOK},
		{"wildcard bind, LAN browser, same-origin", GuardOpts{UnsafeNetwork: true, BoundAddr: "0.0.0.0:8787"}, "192.168.1.5:8787", "http://192.168.1.5:8787", http.StatusOK},
		{"wildcard bind, still loopback-friendly", GuardOpts{UnsafeNetwork: true, BoundAddr: "0.0.0.0:8787"}, "127.0.0.1:8787", "http://127.0.0.1:8787", http.StatusOK},
		{"wildcard bind, hostile page STILL refused", GuardOpts{UnsafeNetwork: true, BoundAddr: "0.0.0.0:8787"}, "192.168.1.5:8787", "http://evil.example", http.StatusForbidden},
		{"wildcard bind, null origin STILL refused", GuardOpts{UnsafeNetwork: true, BoundAddr: "0.0.0.0:8787"}, "192.168.1.5:8787", "null", http.StatusForbidden},

		// bound to one address: the Host is pinned to it (plus loopback), so rebinding
		// against the LAN deployment is still refused.
		{"pinned bind, its own address", GuardOpts{UnsafeNetwork: true, BoundAddr: "192.168.1.5:8787"}, "192.168.1.5:8787", "", http.StatusOK},
		{"pinned bind, loopback still fine", GuardOpts{UnsafeNetwork: true, BoundAddr: "192.168.1.5:8787"}, "127.0.0.1:8787", "", http.StatusOK},
		{"pinned bind, rebound host refused", GuardOpts{UnsafeNetwork: true, BoundAddr: "192.168.1.5:8787"}, "evil.example:8787", "", http.StatusForbidden},

		// the flag is off by default: the same LAN request is refused.
		{"without the flag, a LAN host is refused", GuardOpts{}, "192.168.1.5:8787", "", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, reached := serveGuarded(t, tc.opts, "POST", "/api/spot", tc.host, tc.origin)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (host %q, origin %q)", rec.Code, tc.want, tc.host, tc.origin)
			}
			if reached != (tc.want == http.StatusOK) {
				t.Errorf("handler reached = %v, want %v", reached, tc.want == http.StatusOK)
			}
		})
	}
}

// A refusal has to say what happened, or the one person who legitimately trips it (a
// homelab user pointing a LAN browser at it) has nothing to go on.
func TestGuardRefusalExplainsItself(t *testing.T) {
	rec, _ := serveGuarded(t, GuardOpts{}, "POST", "/api/quit", "127.0.0.1:8787", "http://evil.example")
	body := rec.Body.String()
	for _, want := range []string{"forbidden", "origin"} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Errorf("refusal body %q does not mention %q", body, want)
		}
	}
}

// serveGuarded runs one request through Guard and reports whether it reached the
// wrapped handler. host == "" means no Host header (an HTTP/1.0 client); origin == ""
// means no Origin header at all, which is the case that must keep working.
func serveGuarded(t *testing.T, o GuardOpts, method, path, host, origin string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	reached := false
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}), o)

	req := httptest.NewRequest(method, path, strings.NewReader(`{"gold_usd":1}`))
	// httptest.NewRequest defaults Host to example.com — always set it explicitly, or
	// every one of these tests is really testing the same rejected host.
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	// The attack shape: a JSON body sent as text/plain, which is CORS-simple and so
	// never preflights. decode() does not care, which is exactly why the guard must.
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reached
}
