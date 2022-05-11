package main

import (
	"log"
	"net"
	"os"
	"strings"

	"github.com/miekg/dns"
	"github.com/pkg/profile"

	"github.com/naiba/nbdns/internal/handler"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/qqwry"
)

var (
	version string = "dev"

	ipdb   *qqwry.QQwry
	config *model.Config
)

func init() {
	log.SetOutput(os.Stdout)

	var err error
	ipdb, err = qqwry.NewQQwry("data/qqwry_lastest.dat")
	if err != nil {
		panic(err)
	}
	config = &model.Config{}
	if err = config.ReadInConfig("data/config.json"); err != nil {
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

	bootstrapHandler := handler.NewHandler(model.StrategyAnyResult, config.Bootstrap, ipdb, config.Debug)

	for i := 0; i < len(config.Upstreams); i++ {
		config.Upstreams[i].InitConnectionPool(config.Debug, bootstrapHandler.LookupIP)
	}
}

func main() {
	addr := "127.0.0.1:8853"
	server := &dns.Server{Addr: addr, Net: "udp"}

	upstreamHandler := handler.NewHandler(config.Strategy, config.Upstreams, ipdb, config.Debug)
	dns.HandleFunc(".", upstreamHandler.HandleRequest)

	log.Println("==== DNS Server ====")
	log.Println("端口:", addr)
	log.Println("模式:", config.StrategyName())
	log.Println("版本:", version)

	if config.Profiling != "" {
		defer profile.Start(profile.ProfilePath("debug"), config.ProfileMode()).Stop()
	}

	server.ListenAndServe()
}
