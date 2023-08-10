package main

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"github.com/miekg/dns"
	"github.com/yl2chen/cidranger"

	"github.com/naiba/nbdns/internal/handler"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/doh"
)

var (
	version string = "dev"

	config   *model.Config
	dataPath = detectDataPath()
)

func init() {
	log.SetOutput(os.Stdout)

	ipRanger := loadIPRanger(dataPath + "china_ip_list.txt")

	config = &model.Config{}
	if err := config.ReadInConfig(dataPath+"/config.json", ipRanger); err != nil {
		panic(err)
	}

	bootstrapHandler := handler.NewHandler(model.StrategyAnyResult, false, config.Bootstrap, config.Debug)

	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].InitConnectionPool(bootstrapHandler.LookupIP)
	}
}

func main() {
	server := &dns.Server{Addr: config.ServeAddr, Net: "udp"}
	serverTCP := &dns.Server{Addr: config.ServeAddr, Net: "tcp"}

	upstreamHandler := handler.NewHandler(config.Strategy, config.BuiltInCache, config.Upstreams, config.Debug)
	dns.HandleFunc(".", upstreamHandler.HandleRequest)

	log.Println("==== DNS Server ====")
	log.Println("端口:", config.ServeAddr)
	log.Println("模式:", config.StrategyName())
	log.Println("数据:", dataPath)
	log.Println("启用内置缓存:", config.BuiltInCache)
	if config.DohServer != nil {
		log.Println("启用 DoH 服务器:", config.DohServer.Host)
	}
	log.Println("版本:", version)

	if config.Profiling {
		debugServerHandler := http.NewServeMux()
		debugServerHandler.HandleFunc("/debug/goroutine", func(w http.ResponseWriter, r *http.Request) {
			profile := pprof.Lookup("goroutine")
			profile.WriteTo(w, 2)
		})
		go http.ListenAndServe(":8854", debugServerHandler)
		log.Println("性能分析: http://0.0.0.0:8854/debug/pprof/heap")
	}

	stopCh := make(chan error)

	go func() {
		stopCh <- server.ListenAndServe()
	}()
	go func() {
		stopCh <- serverTCP.ListenAndServe()
	}()
	if config.DohServer != nil {
		dohServer := doh.NewServer(config.DohServer.Host, config.DohServer.Username, config.DohServer.Password, upstreamHandler.Exchange)
		stopCh <- dohServer.Serve()
	}

	log.Printf("server stopped: %+v", <-stopCh)
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
