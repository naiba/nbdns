package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestMatchListsShareOneManagerPerEntry(t *testing.T) {
	dataPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataPath, "cn.conf"), []byte("server=/listed.example/114.114.114.114\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var config model.Config
	if err := json.Unmarshal([]byte(`{"upstreams":[
		{"address":"udp://192.0.2.1:53","match_lists":["cn.conf"]},
		{"address":"udp://192.0.2.2:53","match_lists":["cn.conf"]},
		{"address":"udp://192.0.2.3:53"}]}`), &config); err != nil {
		t.Fatal(err)
	}
	for _, up := range config.Upstreams {
		up.Init(&config, logger.New(false))
	}
	lists, err := loadMatchLists(dataPath, config.Upstreams)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists.managers) != 1 {
		t.Fatalf("managers = %d, want one shared manager (one download, one cache file)", len(lists.managers))
	}
	for i, want := range []bool{true, true, false} {
		if got := config.Upstreams[i].IsMatch("www.listed.example."); got != want {
			t.Errorf("upstream %d IsMatch = %v, want %v", i, got, want)
		}
	}
	sources := lists.Snapshot()
	if len(sources) != 1 || sources[0].Rules != 1 || sources[0].Source != "cn.conf" {
		t.Errorf("status = %+v", sources)
	}
}

func TestMatchListsAbsentWhenNotConfigured(t *testing.T) {
	up := &model.Upstream{Address: "udp://192.0.2.1:53"}
	up.Init(&model.Config{}, logger.New(false))
	lists, err := loadMatchLists(t.TempDir(), []*model.Upstream{up})
	if err != nil || len(lists.managers) != 0 || len(lists.Snapshot()) != 0 {
		t.Fatalf("lists=%+v err=%v", lists, err)
	}
}

func TestMatchListsRejectInsecureSubscription(t *testing.T) {
	up := &model.Upstream{Address: "udp://192.0.2.1:53", MatchLists: []string{"http://lists.example/cn.conf"}}
	up.Init(&model.Config{}, logger.New(false))
	if _, err := loadMatchLists(t.TempDir(), []*model.Upstream{up}); err == nil {
		t.Error("plain HTTP subscriptions must be rejected like filter_lists")
	}
}
