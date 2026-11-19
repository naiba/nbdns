package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/naiba/nbdns/pkg/logger"
)

type staticDomainSet map[string]bool

func (s staticDomainSet) Contains(name string) bool { return s[strings.ToLower(name)] }

func TestUpstreamMatchListsExtendInlineRules(t *testing.T) {
	var up Upstream
	if err := json.Unmarshal([]byte(`{"address":"udp://192.0.2.53:53","match":[".inline.example"],"match_lists":["https://lists.example/cn.conf"]}`), &up); err != nil {
		t.Fatal(err)
	}
	up.Init(&Config{Timeout: 2}, logger.New(false))
	if !up.HasMatchRules() {
		t.Fatal("an upstream with match_lists is a dedicated upstream")
	}
	if !up.IsMatch("a.inline.example.") || up.IsMatch("listed.example.") {
		t.Fatal("before lists load only inline rules may match")
	}
	up.SetMatchLists(staticDomainSet{"listed.example.": true})
	if !up.IsMatch("listed.example.") || !up.IsMatch("a.inline.example.") || up.IsMatch("other.example.") {
		t.Error("lists must add to, not replace, inline match rules")
	}
}

func TestUpstreamWithoutRulesIsDefault(t *testing.T) {
	up := &Upstream{Address: "udp://192.0.2.53:53"}
	up.Init(&Config{Timeout: 2}, logger.New(false))
	if up.HasMatchRules() || up.IsMatch("anything.example.") {
		t.Error("an upstream without match or match_lists is a default upstream")
	}
}

func TestMatchListEntriesValidated(t *testing.T) {
	for _, raw := range []string{`{"address":"udp://192.0.2.53:53","match_lists":[""]}`} {
		var config Config
		if err := json.Unmarshal([]byte(`{"upstreams":[`+raw+`]}`), &config); err != nil {
			t.Fatal(err)
		}
		config.Upstreams[0].Init(&config, logger.New(false))
		if err := config.Upstreams[0].Validate(); err == nil {
			t.Errorf("%s: empty match list entry accepted", raw)
		}
	}
}
