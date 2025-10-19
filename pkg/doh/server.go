package doh

import (
	"encoding/base64"
	"net/http"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/stats"
)

type DoHServer struct {
	username, password string
	handler            func(req *dns.Msg, clientIP, domain string) *dns.Msg
	stats              stats.StatsRecorder
}

func NewServer(username, password string, handler func(req *dns.Msg, clientIP, domain string) *dns.Msg, statsRecorder stats.StatsRecorder) *DoHServer {
	return &DoHServer{
		username: username,
		password: password,
		handler:  handler,
		stats:    statsRecorder,
	}
}

// RegisterRoutes 注册 DoH 路由到现有的 HTTP 服务器
func (s *DoHServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/dns-query", s.handleQuery)
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

	data, err := base64.RawURLEncoding.DecodeString(query)
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

	// 记录 DoH 查询统计
	if s.stats != nil {
		s.stats.RecordDoHQuery()
	}

	// 提取客户端 IP
	clientIP := r.RemoteAddr
	// 如果有 X-Forwarded-For 或 X-Real-IP 头，使用它们
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		clientIP = xff
	} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
		clientIP = xri
	}

	// 提取域名
	var domain string
	if len(msg.Question) > 0 {
		domain = msg.Question[0].Name
	}

	resp := s.handler(msg, clientIP, domain)
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
