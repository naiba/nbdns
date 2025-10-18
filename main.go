package main

import (
	"errors"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blang/semver"
	"github.com/miekg/dns"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
	"github.com/yl2chen/cidranger"

	"github.com/naiba/nbdns/internal/handler"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/internal/web"
	"github.com/naiba/nbdns/pkg/doh"
	"github.com/naiba/nbdns/pkg/logger"
)

var (
	version string

	config   *model.Config
	dataPath string
)

func main() {
	dataPath = detectDataPath()

	ipRanger := loadIPRanger(dataPath + "china_ip_list.txt")

	// 先创建一个临时 logger 用于读取配置
	tempLogger := logger.New(false)

	config = &model.Config{}
	if err := config.ReadInConfig(dataPath+"/config.json", ipRanger, tempLogger); err != nil {
		panic(err)
	}

	// 设置默认 Web 监听地址
	if config.WebAddr == "" {
		config.WebAddr = "0.0.0.0:8854"
	}

	// 根据配置创建正式的 logger 和 stats 实例
	log := logger.New(config.Debug)
	statsRecorder := stats.NewStats()

	// 重新初始化 upstreams 以使用正确的 logger
	for i := 0; i < len(config.Bootstrap); i++ {
		config.Bootstrap[i].Init(config, ipRanger, log)
		config.Bootstrap[i].InitConnectionPool(nil)
	}
	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].Init(config, ipRanger, log)
	}

	// Bootstrap handler 不需要缓存，只是用于初始化连接
	bootstrapHandler := handler.NewHandler(model.StrategyAnyResult, false, config.Bootstrap, dataPath, log, nil)

	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].InitConnectionPool(bootstrapHandler.LookupIP)
	}

	server := &dns.Server{Addr: config.ServeAddr, Net: "udp"}
	serverTCP := &dns.Server{Addr: config.ServeAddr, Net: "tcp"}

	// 只有 upstream handler 需要缓存
	upstreamHandler := handler.NewHandler(config.Strategy, config.BuiltInCache, config.Upstreams, dataPath, log, statsRecorder)
	dns.HandleFunc(".", upstreamHandler.HandleRequest)

	// Setup graceful shutdown
	defer func() {
		if err := upstreamHandler.Close(); err != nil {
			log.Printf("Error closing cache: %v", err)
		}
	}()

	log.Println("==== DNS Server ====")
	log.Println("端口:", config.ServeAddr)
	log.Println("模式:", config.StrategyName())
	log.Println("数据:", dataPath)
	if config.BuiltInCache {
		log.Println("启用 BadgerDB 缓存: 最大 40MB")
	} else {
		log.Println("禁用缓存")
	}

	if config.DohServer != nil {
		log.Println("启用 DoH 服务器:", config.DohServer.Host)
	}
	log.Println("版本:", version)

	// 创建更新检查通道
	checkUpdateCh := make(chan struct{}, 1)

	// 启动 Web 服务（监控面板 + pprof）
	webServerHandler := http.NewServeMux()

	// 注册监控面板路由
	webHandler := web.NewHandler(statsRecorder, version, checkUpdateCh, log)
	webHandler.RegisterRoutes(webServerHandler)

	// 如果启用 profiling，注册 pprof 路由
	if config.Profiling {
		webServerHandler.HandleFunc("/debug/", http.DefaultServeMux.ServeHTTP)
		log.Printf("性能分析: http://%s/debug/pprof/", config.WebAddr)
	}

	go http.ListenAndServe(config.WebAddr, webServerHandler)
	log.Printf("监控面板: http://%s/", config.WebAddr)

	// Start cache statistics logging if cache is enabled
	if config.BuiltInCache {
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				log.Printf("Cache Stats: %s", upstreamHandler.GetCacheStats())
			}
		}()
	}

	stopCh := make(chan error)

	// 启动后台更新检查
	go checkUpdate(checkUpdateCh, stopCh, log)

	// 定时触发更新检查（生产者1：定时器）
	go func() {
		// 启动时立即检查一次
		select {
		case checkUpdateCh <- struct{}{}:
		default:
		}

		// 定时检查
		ticker := time.NewTicker(time.Duration(40+rand.Intn(20)) * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			select {
			case checkUpdateCh <- struct{}{}:
			default:
				// 如果通道已满，跳过本次
			}
		}
	}()

	go func() {
		stopCh <- server.ListenAndServe()
	}()
	go func() {
		stopCh <- serverTCP.ListenAndServe()
	}()

	if config.DohServer != nil {
		go func() {
			dohServer := doh.NewServer(config.DohServer.Host, config.DohServer.Username, config.DohServer.Password, upstreamHandler.Exchange)
			stopCh <- dohServer.Serve()
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		stopCh <- errors.New("shutdown signal received")
	}()

	log.Printf("server stopped: %+v", <-stopCh)
}

// checkUpdate 监听 channel 触发更新检查
func checkUpdate(checkCh <-chan struct{}, stopCh chan<- error, log logger.Logger) {
	for range checkCh {
		// 如果 version 为空，使用默认值
		ver := version
		if ver == "" {
			ver = "0.0.0"
		}
		v := semver.MustParse(ver)
		latest, err := selfupdate.UpdateSelf(v, "naiba/nbdns")
		if err != nil {
			log.Printf("Error checking for updates: %v", err)
			continue
		}
		if latest.Version.Equals(v) {
			log.Printf("No update available, current version: %s", v)
		} else {
			log.Printf("Updated to version: %s", latest.Version)
			stopCh <- errors.New("Server upgraded to " + latest.Version.String())
			return
		}
	}
}

func loadIPRanger(path string) cidranger.Ranger {
	ipRanger := cidranger.NewPCTrieRanger()

	content, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	lines := strings.Split(string(content), "\n")

	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		_, network, err := net.ParseCIDR(lines[i])
		if err != nil {
			panic(err)
		}
		if err := ipRanger.Insert(cidranger.NewBasicRangerEntry(*network)); err != nil {
			panic(err)
		}
	}

	return ipRanger
}

func detectDataPath() string {
	ex, err := os.Executable()
	if err != nil {
		panic(err)
	}
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	pathList := []string{filepath.Dir(ex), pwd}

	for _, path := range pathList {
		if f, err := os.Stat(path + "/data/china_ip_list.txt"); err == nil {
			if f.Size() == 1024*200 {
				panic("离线IP库 china_ip_list.txt 文件损坏，请重新下载")
			}
			return path + "/data/"
		}
	}

	panic("没有检测到数据目录")
}
