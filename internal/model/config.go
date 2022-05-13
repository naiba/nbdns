package model

import (
	"encoding/json"
	"io/ioutil"
	"net"

	"github.com/pkg/errors"
)

const (
	_ = iota
	StrategyFullest
	StrategyFastest
	StrategyAnyResult
)

type Config struct {
	ServeAddr string     `json:"serve_addr,omitempty"`
	Strategy  int        `json:"strategy,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`
	Bootstrap []Upstream `json:"bootstrap,omitempty"`

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
	for i := 0; i < len(c.Bootstrap); i++ {
		c.Bootstrap[i].Init(c.Debug)
		if net.ParseIP(c.Bootstrap[i].hos) == nil {
			return errors.New("Bootstrap 服务器只能使用 IP: " + c.Bootstrap[i].Address)
		}
		c.Bootstrap[i].InitConnectionPool(nil)
	}
	for i := 0; i < len(c.Upstreams); i++ {
		c.Upstreams[i].Init(c.Debug)
		if err := c.Upstreams[i].Validate(); err != nil {
			return err
		}
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
