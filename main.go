package main

import (
	"io/ioutil"
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

	bootstrapHandler := handler.NewHandler(model.StrategyAnyResult, config.Bootstrap, config.Debug)

	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].InitConnectionPool(bootstrapHandler.LookupIP)
	}
}

func main() {
	server := &dns.Server{Addr: config.ServeAddr, Net: "udp"}

	upstreamHandler := handler.NewHandler(config.Strategy, config.Upstreams, config.Debug)
	dns.HandleFunc(".", upstreamHandler.HandleRequest)

	log.Println("==== DNS Server ====")
	log.Println("端口:", config.ServeAddr)
	log.Println("模式:", config.StrategyName())
	log.Println("数据:", dataPath)
	log.Println("版本:", version)

	if config.Profiling {
		http.HandleFunc("/debug/goroutine", func(w http.ResponseWriter, r *http.Request) {
			profile := pprof.Lookup("goroutine")
			profile.WriteTo(w, 2)
		})
		go http.ListenAndServe(":8854", nil)
		log.Println("性能分析: http://0.0.0.0:8854/debug/pprof/heap")
	}

	server.ListenAndServe()
}

func loadIPRanger(path string) cidranger.Ranger {
	ipRanger := cidranger.NewPCTrieRanger()

	content, err := ioutil.ReadFile(path)
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
