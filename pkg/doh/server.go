package doh

import (
	"encoding/base64"
	"net"
	"net/http"
	"strings"

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
	clientIP := extractClientIP(r)

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

// extractClientIP 从 HTTP 请求中提取真实的客户端 IP
func extractClientIP(r *http.Request) string {
	// 1. 优先检查 X-Forwarded-For（适用于多层代理）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For 格式: client, proxy1, proxy2
		// 取第一个 IP（最原始的客户端 IP）
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			clientIP := strings.TrimSpace(parts[0])
			// 验证是否为有效 IP
			if ip := net.ParseIP(clientIP); ip != nil {
				return clientIP
			}
		}
	}

	// 2. 检查 X-Real-IP（单层代理常用）
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return xri
		}
	}

	// 3. 使用 RemoteAddr，需要去掉端口号
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	// 4. 如果无法解析端口，直接返回（可能已经是纯 IP）
	return r.RemoteAddr
}
