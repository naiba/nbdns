package model

import (
	"fmt"
	"github.com/naiba/nbdns/pkg/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigECSValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, wantError string }{
		{"global", `{"ecs":"180.152.0.123/24","upstreams":[{"address":"https://dns.google/dns-query"}]}`, ""},
		{"invalid global", `{"ecs":"bad","upstreams":[{"address":"https://dns.google/dns-query"}]}`, "ECS"},
		{"invalid override", `{"upstreams":[{"address":"https://dns.google/dns-query","ecs":"180.152.0.0/33"}]}`, "ECS"},
		{"no upstream", `{}`, "upstream"},
		{"udp socks", `{"socks_proxy":"127.0.0.1:1080","upstreams":[{"address":"udp://8.8.8.8:53","proxy_enabled":true}]}`, "SOCKS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			var config Config
			err := config.ReadInConfig(path, logger.New(false))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error=%v want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.Upstreams[0].ecsPrefix.String() != "180.152.0.0/24" {
				t.Fatal("global ECS not normalized/inherited")
			}
		})
	}
}

func TestConfigQueryConcurrencyLimit(t *testing.T) {
	for _, tc := range []struct {
		value   int
		want    int
		invalid bool
	}{{0, DefaultMaxConcurrentQueries, false}, {2, 2, false}, {-1, 0, true}} {
		path := filepath.Join(t.TempDir(), "config.json")
		body := fmt.Sprintf(`{"max_concurrent_queries":%d,"upstreams":[{"address":"udp://127.0.0.1:53"}]}`, tc.value)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		var config Config
		err := config.ReadInConfig(path, logger.New(false))
		if tc.invalid {
			if err == nil || !strings.Contains(err.Error(), "max_concurrent_queries") {
				t.Fatalf("invalid limit accepted: %v", err)
			}
			continue
		}
		if err != nil || config.MaxConcurrentQueries != tc.want {
			t.Fatalf("limit=%d want=%d err=%v", config.MaxConcurrentQueries, tc.want, err)
		}
	}
}

func TestConfigDashboardAccessSettings(t *testing.T) {
	for _, tc := range []struct{ name, body, wantError string }{
		{"complete", `{"web_auth":{"username":"admin","password":"secret"},"web_allowed_hosts":["dns.example.com"],"client_name_server":"192.0.2.1","upstreams":[{"address":"udp://127.0.0.1:53"}]}`, ""},
		{"missing password", `{"web_auth":{"username":"admin"},"upstreams":[{"address":"udp://127.0.0.1:53"}]}`, "web_auth"},
		{"missing username", `{"web_auth":{"password":"secret"},"upstreams":[{"address":"udp://127.0.0.1:53"}]}`, "web_auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			var config Config
			err := config.ReadInConfig(path, logger.New(false))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error=%v want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.WebAuth.Username != "admin" || config.WebAuth.Password != "secret" || len(config.WebAllowedHosts) != 1 || config.ClientNameServer != "192.0.2.1" {
				t.Errorf("parsed config: web_auth=%+v hosts=%v client_name_server=%q", config.WebAuth, config.WebAllowedHosts, config.ClientNameServer)
			}
		})
	}
}
