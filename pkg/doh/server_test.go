package doh

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miekg/dns"
)

func TestDoHGETAndPOST(t *testing.T) {
	s := NewServer("", "", func(q *dns.Msg, _, _ string) *dns.Msg {
		return new(dns.Msg).SetReply(q)
	}, nil)
	q := new(dns.Msg)
	q.SetQuestion("example.org.", dns.TypeA)
	q.Id = 0
	wired, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"GET without Accept", httptest.NewRequest(http.MethodGet, "/dns-query?dns="+base64.RawURLEncoding.EncodeToString(wired), nil)},
		{"POST", httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(wired))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.req.Method == http.MethodPost {
				tc.req.Header.Set("Content-Type", dohMediaType)
			}
			w := httptest.NewRecorder()
			s.handleQuery(w, tc.req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			resp := new(dns.Msg)
			if err := resp.Unpack(w.Body.Bytes()); err != nil || !resp.Response {
				t.Errorf("invalid DNS response: %v, %+v", err, resp)
			}
		})
	}
}
