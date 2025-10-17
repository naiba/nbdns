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
	"github.com/naiba/nbdns/internal/singleton"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/internal/web"
	"github.com/naiba/nbdns/pkg/doh"
)

var (
	version string

	config   *model.Config
	dataPath string
)

func main() {
	dataPath = detectDataPath()

	ipRanger := loadIPRanger(dataPath + "china_ip_list.txt")

	config = &model.Config{}
	if err := config.ReadInConfig(dataPath+"/config.json", ipRanger); err != nil {
		panic(err)
	}

	singleton.InitLogger(config.Debug)

	// 初始化统计系统
	stats.Init()

	// Bootstrap handler 不需要缓存，只是用于初始化连接
	bootstrapHandler := handler.NewHandler(model.StrategyAnyResult, false, config.Bootstrap, dataPath)

	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].InitConnectionPool(bootstrapHandler.LookupIP)
	}

	server := &dns.Server{Addr: config.ServeAddr, Net: "udp"}
	serverTCP := &dns.Server{Addr: config.ServeAddr, Net: "tcp"}

	// 只有 upstream handler 需要缓存
	upstreamHandler := handler.NewHandler(config.Strategy, config.BuiltInCache, config.Upstreams, dataPath)
	dns.HandleFunc(".", upstreamHandler.HandleRequest)

	// Setup graceful shutdown
	defer func() {
		if err := upstreamHandler.Close(); err != nil {
			singleton.Logger.Printf("Error closing cache: %v", err)
		}
	}()

	singleton.Logger.Println("==== DNS Server ====")
	singleton.Logger.Println("端口:", config.ServeAddr)
	singleton.Logger.Println("模式:", config.StrategyName())
	singleton.Logger.Println("数据:", dataPath)
	if config.BuiltInCache {
		singleton.Logger.Println("启用 BadgerDB 缓存: 最大 40MB")
	} else {
		singleton.Logger.Println("禁用缓存")
	}

	if config.DohServer != nil {
		singleton.Logger.Println("启用 DoH 服务器:", config.DohServer.Host)
	}
	singleton.Logger.Println("版本:", version)

	// 启动 Web 服务（监控面板 + pprof）
	webServerHandler := http.NewServeMux()

	// 注册监控面板路由
	webHandler := web.NewHandler(stats.GlobalStats)
	webHandler.RegisterRoutes(webServerHandler)

	// 如果启用 profiling，注册 pprof 路由
	if config.Profiling {
		webServerHandler.HandleFunc("/debug/", http.DefaultServeMux.ServeHTTP)
		singleton.Logger.Println("性能分析: http://0.0.0.0:8854/debug/pprof/")
	}

	go http.ListenAndServe(":8854", webServerHandler)
	singleton.Logger.Println("监控面板: http://0.0.0.0:8854/")

	// Start cache statistics logging if cache is enabled
	if config.BuiltInCache {
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				singleton.Logger.Printf("Cache Stats: %s", upstreamHandler.GetCacheStats())
			}
		}()
	}

	stopCh := make(chan error)
	go checkUpdate(stopCh)

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
		singleton.Logger.Println("Shutting down...")
		stopCh <- errors.New("shutdown signal received")
	}()

	singleton.Logger.Printf("server stopped: %+v", <-stopCh)
}

func checkUpdate(stopCh chan<- error) {
	for {
		go func() {
			// 如果 version 为空，使用默认值
			ver := version
			if ver == "" {
				ver = "0.0.0"
			}
			v := semver.MustParse(ver)
			latest, err := selfupdate.UpdateSelf(v, "naiba/nbdns")
			if err != nil {
				singleton.Logger.Printf("Error checking for updates: %v", err)
				return
			}
			if latest.Version.Equals(v) {
				singleton.Logger.Printf("No update available, current version: %s", v)
			} else {
				singleton.Logger.Printf("Updated to version: %s", latest.Version)
				stopCh <- errors.New("Server upgraded to " + latest.Version.String())
			}
		}()
		time.Sleep(time.Duration(40+rand.Intn(20)) * time.Minute)
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
