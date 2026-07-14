package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The loopback guard (om-6ex5).
//
// Binding to 127.0.0.1 is the right default for a single-user local app, but it is not
// an authentication boundary — it only means the attacker has to be a webpage instead of
// a stranger on the internet. Every page the user has open can reach us, and the API has
// no auth at all.
//
// The reason CORS does not already save us: decode() (api.go) never inspects
// Content-Type. A hostile page can therefore send a JSON body as Content-Type:
// text/plain — a CORS-safelisted type, so the request is "simple" and never preflights —
// and Go parses it happily. That puts EVERY POST route in reach of any open tab:
// /api/quit, /api/lots/{id}/sell, /api/branches/{id}/merge, /api/spot, and the generic
// table creates. The response is unreadable cross-origin, but the write lands: a blind
// `for id in 1..500: sell` loop against a dense integer rowid is silent, irreversible
// corruption of a financial ledger. (PUT/DELETE always preflight, and the preflight
// fails, so they were only ever safe by accident.)
//
// So: check the Origin. A browser cannot forge it and cannot suppress it on a
// cross-origin request, which makes an Origin check complete coverage of the actual
// adversary — no per-launch token needed (and a token would only defend against a local
// non-browser process, which can already read crh.db straight off disk).
//
// The three rules, and why each is the way it is:
//
//   - A MISSING Origin is ALLOWED. curl, the Go health probe in instanceAt(), and the
//     Node-side e2e fetches send none. The threat model loses nothing: the browser is
//     the thing that cannot omit it.
//   - The Origin/Host HOSTNAME must be loopback. Not the exact address: the Vite dev
//     proxy forwards Origin: http://localhost:5173, and the app itself may be on
//     localhost, 127.0.0.1 or ::1 depending on how it was reached.
//   - The PORT is never pinned. The ephemeral-port fallback (launch.go) moves it, the
//     dev proxy is on another one entirely, and qa/run.sh takes a PORT.
//
// The Host check is the second half, and it is not optional: DNS rebinding makes a
// hostile page same-origin with us (so it can READ, which is the only path to actual
// exfiltration) — but the rebound request still carries Host: evil.example, which is not
// loopback. That is why the Host check applies to GET too.

// GuardOpts configures Guard. The zero value is the ordinary local app: loopback only.
type GuardOpts struct {
	// UnsafeNetwork relaxes the HOST check for a deliberate non-loopback bind
	// (`serve --addr 0.0.0.0:8787 --unsafe-network`). It does NOT relax the Origin
	// check: a cross-origin request is still refused, because the hostile-webpage
	// threat does not go away just because the operator wanted LAN access.
	UnsafeNetwork bool
	// BoundAddr is the address the server actually bound (ln.Addr()). Under
	// UnsafeNetwork it is what the Host check pins to — unless it is a wildcard, which
	// names no single address and so cannot pin anything.
	BoundAddr string
}

// Guard wraps next with the Origin/Host check.
//
// It must wrap the OUTER mux — the one that carries POST /api/quit, which is registered
// in cmd/, not in Handler. A guard applied inside Handler would miss the exact endpoint
// that stops the process.
func Guard(next http.Handler, o GuardOpts) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, o) {
			forbid(w, "This request arrived under a host name that is not this machine.")
			return
		}
		if !originAllowed(r.Header.Get("Origin"), r.Host, o) {
			forbid(w, "This request came from another website's origin.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func forbid(w http.ResponseWriter, why string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	// Deliberately no Access-Control-Allow-Origin: the caller must not get to read this
	// either. The wording is for the one person who trips it honestly — a homelab user
	// pointing a LAN browser at it — not for the attacker.
	w.Write([]byte("403 forbidden: CoinRollHunter only answers your own computer.\n" +
		why + "\n" +
		"If you meant to reach it over a network, start it with --addr <address> --unsafe-network.\n"))
}

// IsLoopbackAddr reports whether a listen address ("127.0.0.1:8787", "localhost:0",
// "[::1]:8787") names this machine and nothing else. A wildcard (":8787", "0.0.0.0:8787")
// does not: it is every interface on the box. The command uses this to decide whether an
// --addr needs the --unsafe-network opt-in.
func IsLoopbackAddr(hostport string) bool {
	return isLoopback(hostnameOf(hostport))
}

// hostAllowed implements the DNS-rebinding half: the name the client used to reach us
// has to be one of ours.
func hostAllowed(host string, o GuardOpts) bool {
	h := hostnameOf(host)
	// No Host header at all — an HTTP/1.0 client. A browser always sends one, and a
	// rebinding attack is defined by the name it puts here, so there is nothing to
	// refuse. Refusing would only break raw clients.
	if h == "" {
		return true
	}
	if isLoopback(h) {
		return true
	}
	if !o.UnsafeNetwork {
		return false
	}
	// The operator asked to be reachable off-box. Pin to the address they bound, so
	// rebinding is still refused; a wildcard bind names no address, so any Host can
	// legitimately reach us and the Origin check carries the weight on its own.
	b := hostnameOf(o.BoundAddr)
	if b == "" || b == "0.0.0.0" || b == "::" {
		return true
	}
	return strings.EqualFold(b, h)
}

// originAllowed implements the hostile-webpage half.
func originAllowed(origin, host string, o GuardOpts) bool {
	// No Origin: not a browser (curl, the Go probe, the Node e2e fetches). Allowed —
	// see the rules above; a browser cannot strip it on a cross-origin request.
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		// Includes the literal "null", which is what a sandboxed iframe, a data: URL or
		// a file:// page sends. An opaque origin is not our page.
		return false
	}
	if isLoopback(u.Hostname()) {
		return true
	}
	if !o.UnsafeNetwork {
		return false
	}
	// Under a LAN bind the app's own page is served from a non-loopback origin, so
	// "same-origin as the host you addressed" is the only thing that can be allowed —
	// and it is still exactly a same-origin check, so evil.example stays out.
	return strings.EqualFold(u.Host, host)
}

// hostnameOf strips the port from a Host header or an addr, and unwraps an IPv6
// literal's brackets. Both forms show up: "127.0.0.1:8787", "[::1]:8787", a bare
// "localhost" (default port), and ":8787" (a wildcard bind).
func hostnameOf(hostport string) string {
	if hostport == "" {
		return ""
	}
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = strings.Trim(hostport, "[]") // no port
	}
	return h
}

// isLoopback reports whether h names this machine — 127.0.0.0/8, ::1, or the literal
// "localhost".
//
// Nothing else, on purpose. A name like "localtest.me" resolves to 127.0.0.1 and would
// happily serve as a rebinding vector; the point of the check is the NAME the request
// carried, not where DNS says it points today.
//
// The trailing-dot normalization lives here, not in hostnameOf, so BOTH callers agree:
// hostAllowed reaches us through hostnameOf, but originAllowed passes url.Hostname()
// straight in, and url.Hostname() does NOT strip the dot. Without this, a page loaded via
// "http://localhost.:8787" would be served (Host ok) but its own fetches would 403
// (Origin mismatch). A trailing dot is a legal absolute FQDN and resolves the same.
func isLoopback(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(h, "localhost")
}
