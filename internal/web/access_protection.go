package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// AccessOptions configures Protect.
type AccessOptions struct {
	// AllowedHosts are extra host names (web_allowed_hosts) the dashboard may be
	// reached by, e.g. a public name behind a reverse proxy.
	AllowedHosts []string
	// Username and Password enable HTTP Basic authentication when both are set.
	Username, Password string
}

// Names under these suffixes are not publicly registrable, so an attacker cannot
// point them at the dashboard for DNS rebinding.
var privateHostSuffixes = []string{".lan", ".local", ".localdomain", ".home", ".home.arpa", ".internal", ".localhost"}

// Protect guards the dashboard, which reveals which client resolved which
// domain and can reset statistics or trigger self-updates:
//   - Host allowlist against DNS rebinding: a rebound attacker page sends its
//     own public name as Host even though it reaches the dashboard address.
//   - Cross-origin protection for state-changing requests (CSRF).
//   - Optional HTTP Basic authentication.
//
// DoH has its own authentication and is not wrapped.
func Protect(next http.Handler, options AccessOptions) http.Handler {
	allowed := make(map[string]bool, len(options.AllowedHosts))
	for _, host := range options.AllowedHosts {
		allowed[strings.ToLower(strings.TrimSuffix(host, "."))] = true
	}
	var wantUser, wantPassword [sha256.Size]byte
	authenticate := options.Username != "" && options.Password != ""
	if authenticate {
		wantUser, wantPassword = sha256.Sum256([]byte(options.Username)), sha256.Sum256([]byte(options.Password))
	}
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, allowed) {
			http.Error(w, "Host not allowed: add it to web_allowed_hosts", http.StatusForbidden)
			return
		}
		if authenticate {
			user, password, ok := r.BasicAuth()
			// Compare fixed-size digests so timing reveals neither content nor length.
			userDigest, passwordDigest := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(password))
			if !ok || subtle.ConstantTimeCompare(userDigest[:], wantUser[:])&subtle.ConstantTimeCompare(passwordDigest[:], wantPassword[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="nbdns", charset="UTF-8"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(guarded)
}

func hostAllowed(hostport string, allowed map[string]bool) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	// A literal address cannot be rebound: the page would be cross-origin.
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if host == "localhost" || !strings.Contains(host, ".") || allowed[host] {
		return true
	}
	for _, suffix := range privateHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
