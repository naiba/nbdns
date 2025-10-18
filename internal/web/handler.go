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
	stats       *stats.Stats
	version     string
	checkUpdateCh chan<- struct{}
}

// NewHandler 创建Web处理器
func NewHandler(s *stats.Stats, ver string, checkCh chan<- struct{}) *Handler {
	return &Handler{
		stats:       s,
		version:     ver,
		checkUpdateCh: checkCh,
	}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// API路由
	mux.HandleFunc("/api/stats", h.handleStats)
	mux.HandleFunc("/api/version", h.handleVersion)
	mux.HandleFunc("/api/check-update", h.handleCheckUpdate)

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

// VersionResponse 版本信息响应
type VersionResponse struct {
	Version string `json:"version"`
}

// handleVersion 处理版本查询请求
func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ver := h.version
	if ver == "" {
		ver = "0.0.0"
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	if err := json.NewEncoder(w).Encode(VersionResponse{Version: ver}); err != nil {
		singleton.Logger.Printf("Error encoding version JSON: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// UpdateCheckResponse 更新检查响应
type UpdateCheckResponse struct {
	HasUpdate      bool   `json:"has_update"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	Message        string `json:"message"`
}

// handleCheckUpdate 处理检查更新请求（生产者2：用户手动触发）
func (h *Handler) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ver := h.version
	if ver == "" {
		ver = "0.0.0"
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// 触发后台检查更新（非阻塞）
	select {
	case h.checkUpdateCh <- struct{}{}:
		singleton.Logger.Printf("Update check triggered by user")
		json.NewEncoder(w).Encode(UpdateCheckResponse{
			HasUpdate:      false,
			CurrentVersion: ver,
			LatestVersion:  ver,
			Message:        "已触发更新检查，请查看服务器日志",
		})
	default:
		// 如果通道已满，说明已经在检查中
		json.NewEncoder(w).Encode(UpdateCheckResponse{
			HasUpdate:      false,
			CurrentVersion: ver,
			LatestVersion:  ver,
			Message:        "更新检查正在进行中",
		})
	}
}
