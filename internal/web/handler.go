package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/naiba/nbdns/internal/singleton"
	"github.com/naiba/nbdns/internal/stats"
)

//go:embed static/*
var staticFiles embed.FS

// Handler Web服务处理器
type Handler struct {
	stats *stats.Stats
}

// NewHandler 创建Web处理器
func NewHandler(s *stats.Stats) *Handler {
	return &Handler{
		stats: s,
	}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// API路由
	mux.HandleFunc("/api/stats", h.handleStats)

	// 静态文件服务
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		singleton.Logger.Printf("Failed to load static files: %v", err)
		return
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
}

// handleStats 处理统计信息请求
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	// 只允许GET请求
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 获取统计快照
	snapshot := h.stats.GetSnapshot()

	// 设置响应头
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// 编码JSON并返回
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		singleton.Logger.Printf("Error encoding stats JSON: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}
