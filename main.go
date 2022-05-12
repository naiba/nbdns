package main

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime/pprof"
	"strings"

	"github.com/miekg/dns"

	"github.com/naiba/nbdns/internal/handler"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/qqwry"
)

var (
	version string = "dev"

	config *model.Config
)

func init() {
	log.SetOutput(os.Stdout)

	if err := qqwry.LoadFile("data/qqwry_lastest.dat"); err != nil {
		panic(err)
	}

	config = &model.Config{}
	if err := config.ReadInConfig("data/config.json"); err != nil {
		panic(err)
	}

	for i := 0; i < len(config.Bootstrap); i++ {
		_, addr, _ := strings.Cut(config.Bootstrap[i].Address, "://")
		ip, _, ok := strings.Cut(addr, ":")
		if !ok || net.ParseIP(ip) == nil {
			log.Panicf("invalid bootstrap address: %s", config.Bootstrap[i].Address)
		}
		config.Bootstrap[i].InitConnectionPool(config.Debug, nil)
	}

	bootstrapHandler := handler.NewHandler(model.StrategyAnyResult, config.Bootstrap, config.Debug)

	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].InitConnectionPool(config.Debug, bootstrapHandler.LookupIP)
	}
}

func main() {
	server := &dns.Server{Addr: config.ServeAddr, Net: "udp"}

	upstreamHandler := handler.NewHandler(config.Strategy, config.Upstreams, config.Debug)
	dns.HandleFunc(".", upstreamHandler.HandleRequest)

	log.Println("==== DNS Server ====")
	log.Println("端口:", config.ServeAddr)
	log.Println("模式:", config.StrategyName())
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
