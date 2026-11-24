package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func dashboardRoutes(t *testing.T) (*http.ServeMux, chan struct{}) {
	t.Helper()
	checks := make(chan struct{}, 1)
	mux := http.NewServeMux()
	NewHandler(stats.NewStats(), "1.0.0", checks, logger.New(false)).RegisterRoutes(mux)
	return mux, checks
}

// The dashboard exposes every resolved domain and the client that asked for it.
// A wildcard CORS header let any web page opened on the LAN read that history.
func TestDashboardAPIsDoNotAllowCrossOriginReads(t *testing.T) {
	mux, _ := dashboardRoutes(t)
	for _, path := range []string{"/api/stats", "/api/version", "/api/filters", "/api/match-lists"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Origin", "https://attacker.example")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, response.Code)
		}
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s Access-Control-Allow-Origin = %q, want none", path, got)
		}
	}
}

// A GET that triggers a self-update can be fired by an <img> on any page.
func TestCheckUpdateRequiresPost(t *testing.T) {
	mux, checks := dashboardRoutes(t)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/check-update", nil))
	if response.Code != http.StatusMethodNotAllowed || len(checks) != 0 {
		t.Errorf("GET check-update: status %d, triggered %d", response.Code, len(checks))
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/check-update", nil))
	if response.Code != http.StatusOK || len(checks) != 1 {
		t.Errorf("POST check-update: status %d, triggered %d", response.Code, len(checks))
	}
}

func protectedDashboard(t *testing.T, options AccessOptions) (http.Handler, *stats.Stats) {
	t.Helper()
	recorder := stats.NewStats()
	mux := http.NewServeMux()
	NewHandler(recorder, "1.0.0", make(chan struct{}, 1), logger.New(false)).RegisterRoutes(mux)
	return Protect(mux, options), recorder
}

// DNS rebinding: a page on attacker.example re-resolves its own name to the
// dashboard address, so the browser treats the dashboard as same-origin. The
// Host header still carries the attacker's name and gives it away.
func TestProtectRejectsUnknownHostNames(t *testing.T) {
	dashboard, _ := protectedDashboard(t, AccessOptions{AllowedHosts: []string{"DNS.Example.com"}})
	cases := map[string]int{
		"192.0.2.53:8854":           http.StatusOK,
		"192.0.2.53":                http.StatusOK,
		"[2001:db8::53]:8854":       http.StatusOK,
		"localhost:8854":            http.StatusOK,
		"nbdns:8854":                http.StatusOK, // Single-label names cannot be registered publicly.
		"router.lan":                http.StatusOK,
		"router.home.arpa":          http.StatusOK,
		"router.local":              http.StatusOK,
		"dns.example.com":           http.StatusOK, // Configured web_allowed_hosts, case-insensitive.
		"attacker.example":          http.StatusForbidden,
		"lan.attacker.example:8854": http.StatusForbidden,
		"":                          http.StatusForbidden,
	}
	for host, want := range cases {
		request := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
		request.Host = host
		response := httptest.NewRecorder()
		dashboard.ServeHTTP(response, request)
		if response.Code != want {
			t.Errorf("Host %q: status %d, want %d", host, response.Code, want)
		}
	}
}

func TestProtectRejectsCrossSiteWrites(t *testing.T) {
	dashboard, recorder := protectedDashboard(t, AccessOptions{})
	post := func(headers map[string]string) int {
		recorder.RecordQuery()
		request := httptest.NewRequest(http.MethodPost, "http://192.0.2.53:8854/api/stats/reset", nil)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		dashboard.ServeHTTP(response, request)
		return response.Code
	}
	for _, headers := range []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Origin": "https://attacker.example"},
	} {
		if code := post(headers); code != http.StatusForbidden || recorder.TotalQueries.Load() == 0 {
			t.Errorf("cross-site reset %v: status %d, total %d", headers, code, recorder.TotalQueries.Load())
		}
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://192.0.2.53:8854"}); code != http.StatusOK || recorder.TotalQueries.Load() != 0 {
		t.Errorf("same-origin reset: status %d, total %d", code, recorder.TotalQueries.Load())
	}
	// Non-browser clients send neither header and keep working.
	if code := post(nil); code != http.StatusOK {
		t.Errorf("plain reset: status %d", code)
	}
}

func TestProtectRequiresConfiguredPassword(t *testing.T) {
	dashboard, _ := protectedDashboard(t, AccessOptions{Username: "admin", Password: "correct horse"})
	cases := []struct {
		user, password string
		set            bool
		want           int
	}{
		{"", "", false, http.StatusUnauthorized},
		{"admin", "wrong", true, http.StatusUnauthorized},
		{"other", "correct horse", true, http.StatusUnauthorized},
		{"admin", "correct horse", true, http.StatusOK},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodGet, "http://192.0.2.53:8854/", nil)
		if tc.set {
			request.SetBasicAuth(tc.user, tc.password)
		}
		response := httptest.NewRecorder()
		dashboard.ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Errorf("%s/%s: status %d, want %d", tc.user, tc.password, response.Code, tc.want)
		}
		if tc.want == http.StatusUnauthorized && response.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s/%s: missing WWW-Authenticate", tc.user, tc.password)
		}
	}
}
