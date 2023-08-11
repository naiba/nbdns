package doh

import (
	"encoding/base64"
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

	accept := r.Header.Get("Accept")
	if accept != dohMediaType {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		w.Write([]byte("unsupported media type: " + accept))
		return
	}

	query := r.URL.Query().Get("dns")
	if query == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	data, err := base64.StdEncoding.DecodeString(query)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	resp := s.handler(msg)
	if resp == nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("nil response"))
		return
	}

	data, err = resp.Pack()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	w.Header().Set("Content-Type", dohMediaType)
	w.Write(data)
}
