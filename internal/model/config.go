package model

import (
	"encoding/json"
	"io/ioutil"
)

const (
	_ = iota
	StrategyFullest
	StrategyFastest
	StrategyAnyResult
)

type Config struct {
	Bootstrap []Upstream `json:"bootstrap,omitempty"`

	Strategy  int        `json:"strategy,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`

	Debug     bool `json:"debug,omitempty"`
	Profiling bool `json:"profiling,omitempty"`
}

func (c *Config) ReadInConfig(path string) error {
	body, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(body), c); err != nil {
		return err
	}
	return nil
}

func (c *Config) StrategyName() string {
	switch c.Strategy {
	case StrategyFullest:
		return "最全结果"
	case StrategyFastest:
		return "最快结果"
	case StrategyAnyResult:
		return "任一结果（建议仅 bootstrap）"
	}
	panic("invalid strategy")
}
