package doh

import (
	"net/http"

	"github.com/miekg/dns"
)

type DoHServer struct {
	host, username, password string
	handler                  func(req *dns.Msg) *dns.Msg
}

func NewServer(host, username, password string, handler func(req *dns.Msg) *dns.Msg) *DoHServer {
	return &DoHServer{
		host:     host,
		username: username,
		password: password,
		handler:  handler,
	}
}

func (s *DoHServer) Serve() error {
	dohHandler := http.NewServeMux()
	dohHandler.HandleFunc("/dns-query", s.handleQuery)
	return http.ListenAndServe(s.host, dohHandler)
}

func (s *DoHServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	if s.username != "" && s.password != "" {
		username, password, ok := r.BasicAuth()
		if !ok || username != s.username || password != s.password {
			w.Header().Set("WWW-Authenticate", `Basic realm="dns"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	query := r.URL.Query().Get("dns")
	if query == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	msg := new(dns.Msg)
	if err := msg.Unpack([]byte(query)); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	resp := s.handler(msg)
	if resp == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	data, err := resp.Pack()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/dns-message")
	w.Write(data)
}
