package handler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/logger"
)

func namespaceForConfig(t *testing.T, configJSON string) string {
	t.Helper()
	var config model.Config
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		t.Fatal(err)
	}
	up := &model.Upstream{Address: "udp://192.0.2.53:53"}
	up.Init(&config, logger.New(false))
	return upstreamCacheNamespace([]*model.Upstream{up})
}

func TestECSBlackholeFallbackCacheNamespace(t *testing.T) {
	// Caches written before ECS blackhole repair may hold blackhole answers
	// (e.g. 169.254.0.2 for up to an hour, and longer as stale
	// fallback). The repairing policy must not read them.
	legacyPolicy := `[{"Address":"udp://192.0.2.53:53","Socks":false,"Match":null,"ECS":"180.152.0.0/24"}]`
	legacyNamespace := fmt.Sprintf("ecs-v1:%x:", sha256.Sum256([]byte(legacyPolicy)))
	enabledDefault := namespaceForConfig(t, `{"ecs":"180.152.0.0/24"}`)
	enabledExplicit := namespaceForConfig(t, `{"ecs":"180.152.0.0/24","ecs_blackhole_fallback":true}`)
	disabled := namespaceForConfig(t, `{"ecs":"180.152.0.0/24","ecs_blackhole_fallback":false}`)
	disabledWithoutECS := namespaceForConfig(t, `{"ecs_blackhole_fallback":false}`)
	withoutECS := namespaceForConfig(t, `{}`)

	if enabledDefault != enabledExplicit {
		t.Errorf("explicitly enabling the default changed the namespace: %s vs %s", enabledDefault, enabledExplicit)
	}
	if disabled == enabledDefault {
		t.Error("disabling the fallback must not reuse caches built with fallback answers")
	}
	if disabledWithoutECS != withoutECS {
		t.Error("the switch is irrelevant without ECS and must not split the namespace")
	}
	if enabledDefault == legacyNamespace {
		t.Error("repairing policy reuses pre-upgrade caches that may contain blackhole answers")
	}
	if disabled != legacyNamespace {
		t.Errorf("disabled repair behaves like the old version and should keep its caches: %s want %s", disabled, legacyNamespace)
	}
	if withoutECS != fmt.Sprintf("ecs-v1:%x:", sha256.Sum256([]byte(`[{"Address":"udp://192.0.2.53:53","Socks":false,"Match":null,"ECS":""}]`))) {
		t.Errorf("policies without ECS cannot hold blackhole answers and should keep their caches: %s", withoutECS)
	}
}
