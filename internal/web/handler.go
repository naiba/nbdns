package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/miekg/dns"

	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/handler"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

//go:embed static/*
var staticFiles embed.FS

// Handler Web服务处理器
type Handler struct {
	stats         stats.StatsRecorder
	version       string
	checkUpdateCh chan<- struct{}
	logger        logger.Logger
	filterStatus  func() filter.Snapshot
	ecsStatus     func() string
	matchLists    func() []filter.SourceStatus
	diagnose      Diagnoser
	clientNames   *ClientNameResolver
}

// Diagnoser explains how a name resolves (handler.Handler.Diagnose).
type Diagnoser func(ctx context.Context, name string, qtype uint16) (handler.Diagnosis, error)

// SetDiagnoser enables /api/diagnose.
func (h *Handler) SetDiagnoser(diagnose Diagnoser) { h.diagnose = diagnose }

// SetClientNameResolver labels client addresses in /api/stats.
func (h *Handler) SetClientNameResolver(resolver *ClientNameResolver) { h.clientNames = resolver }

func (h *Handler) SetFilterStatus(snapshot func() filter.Snapshot) {
	h.filterStatus = snapshot
}

// SetMatchListStatus reports the upstream domain list subscriptions.
func (h *Handler) SetMatchListStatus(status func() []filter.SourceStatus) {
	h.matchLists = status
}

// SetECSBlackholeStatus reports whether ECS blackhole fallback is active.
func (h *Handler) SetECSBlackholeStatus(status func() string) {
	h.ecsStatus = status
}

// NewHandler 创建Web处理器
func NewHandler(s stats.StatsRecorder, ver string, checkCh chan<- struct{}, log logger.Logger) *Handler {
	return &Handler{
		stats:         s,
		version:       ver,
		checkUpdateCh: checkCh,
		logger:        log,
	}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// API路由
	mux.HandleFunc("/api/stats", h.handleStats)
	mux.HandleFunc("/api/filters", h.handleFilters)
	mux.HandleFunc("/api/match-lists", h.handleMatchLists)
	mux.HandleFunc("/api/stats/reset", h.handleStatsReset)
	mux.HandleFunc("/api/version", h.handleVersion)
	mux.HandleFunc("/api/check-update", h.handleCheckUpdate)
	mux.HandleFunc("/api/diagnose", h.handleDiagnose)

	// 静态文件服务
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		h.logger.Printf("Failed to load static files: %v", err)
		return
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
}

func (h *Handler) handleFilters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	snapshot := filter.Snapshot{Sources: []filter.SourceStatus{}}
	if h.filterStatus != nil {
		snapshot = h.filterStatus()
	}
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (h *Handler) handleMatchLists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	sources := []filter.SourceStatus{}
	if h.matchLists != nil {
		sources = h.matchLists()
	}
	_ = json.NewEncoder(w).Encode(struct {
		Sources []filter.SourceStatus `json:"sources"`
	}{sources})
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
	if h.ecsStatus != nil {
		snapshot.ECSBlackhole.Status = h.ecsStatus()
	}

	// 设置响应头
	// 不设置 Access-Control-Allow-Origin：曾经的 "*" 让局域网内任意网页都能读走
	// 全部解析记录和客户端地址。面板与 API 同源，不需要 CORS。
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	response := statsResponse{StatsSnapshot: snapshot}
	if h.clientNames != nil {
		response.ClientNames = h.clientNames.Names(snapshotClients(snapshot))
	}

	// 编码JSON并返回
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Printf("Error encoding stats JSON: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// statsResponse adds PTR names of client addresses to the statistics.
type statsResponse struct {
	stats.StatsSnapshot
	ClientNames map[string]string `json:"client_names,omitempty"`
}

func snapshotClients(snapshot stats.StatsSnapshot) []string {
	seen := make(map[string]bool)
	var clients []string
	add := func(client string) {
		if client != "" && !seen[client] {
			seen[client] = true
			clients = append(clients, client)
		}
	}
	for _, item := range snapshot.TopClients {
		add(item.Key)
	}
	for _, ranking := range [][]stats.TopNItemJSON{snapshot.TopDomains, snapshot.TopBlockedDomains,
		snapshot.TopPrivateAnswerDomains, snapshot.TopFailedDomains} {
		for _, item := range ranking {
			for _, client := range item.TopClients {
				add(client.Client)
			}
		}
	}
	return clients
}

func (h *Handler) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.diagnose == nil {
		http.Error(w, "diagnosis unavailable", http.StatusServiceUnavailable)
		return
	}
	qtype := dns.TypeA
	if name := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("type"))); name != "" {
		var ok bool
		if qtype, ok = dns.StringToType[name]; !ok {
			http.Error(w, "unknown record type", http.StatusBadRequest)
			return
		}
	}
	result, err := h.diagnose(r.Context(), r.URL.Query().Get("name"), qtype)
	if errors.Is(err, handler.ErrInvalidDiagnosisName) {
		http.Error(w, "invalid domain name", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}

// ResetResponse 重置响应
type ResetResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// handleStatsReset 处理统计数据重置请求
func (h *Handler) handleStatsReset(w http.ResponseWriter, r *http.Request) {
	// 只允许POST请求
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 重置统计数据
	h.stats.Reset()
	h.logger.Printf("Statistics reset by user request")

	// 设置响应头
	w.Header().Set("Content-Type", "application/json")

	// 返回成功响应
	if err := json.NewEncoder(w).Encode(ResetResponse{
		Success: true,
		Message: "统计数据已重置",
	}); err != nil {
		h.logger.Printf("Error encoding reset response JSON: %v", err)
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
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	if err := json.NewEncoder(w).Encode(VersionResponse{Version: ver}); err != nil {
		h.logger.Printf("Error encoding version JSON: %v", err)
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
	// 触发更新是写操作：GET 时任意网页用一个 <img> 就能触发，且绕过跨站写保护。
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ver := h.version
	if ver == "" {
		ver = "0.0.0"
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// 触发后台检查更新（非阻塞）
	select {
	case h.checkUpdateCh <- struct{}{}:
		h.logger.Printf("Update check triggered by user")
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
