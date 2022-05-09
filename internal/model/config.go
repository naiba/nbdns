package model

import (
	"encoding/json"
	"io/ioutil"

	"github.com/pkg/profile"
)

const (
	_ = iota
	StrategyFullest
	StrategyFastest
)

type Config struct {
	Upstreams []Upstream `json:"upstreams,omitempty"`
	Strategy  int        `json:"strategy,omitempty"`
	Debug     bool       `json:"debug,omitempty"`
	Profiling string     `json:"profiling,omitempty"`
}

func (c *Config) ReadInConfig(path string) error {
	body, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(body), c); err != nil {
		return err
	}
	for i := 0; i < len(c.Upstreams); i++ {
		c.Upstreams[i].InitConnectionPool(c.Debug)
	}
	return nil
}

func (c *Config) StrategyName() string {
	switch c.Strategy {
	case StrategyFullest:
		return "最全结果"
	case StrategyFastest:
		return "最快结果"
	}
	panic("invalid strategy")
}

func (c *Config) ProfileMode() func(*profile.Profile) {
	switch c.Profiling {
	case "cpu":
		return profile.CPUProfile
	case "mem":
		return profile.MemProfile
	case "alloc":
		return profile.MemProfileAllocs
	}
	panic("invalid profiling mode")
}
