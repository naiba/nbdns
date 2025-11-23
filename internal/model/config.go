package model

import (
	"encoding/json"
	"net"
	"os"

	"github.com/naiba/nbdns/pkg/logger"
	"github.com/naiba/nbdns/pkg/utils"
	"github.com/pkg/errors"
	"github.com/yl2chen/cidranger"
	"golang.org/x/net/proxy"
)

const (
	_ = iota
	StrategyFullest
	StrategyFastest
	StrategyAnyResult
)

type DohServerConfig struct {
	Username string `json:"username,omitempty"` // DoH Basic Auth 用户名（可选）
	Password string `json:"password,omitempty"` // DoH Basic Auth 密码（可选）
}

type Config struct {
	ServeAddr    string           `json:"serve_addr,omitempty"`
	WebAddr      string           `json:"web_addr,omitempty"`
	DohServer    *DohServerConfig `json:"doh_server,omitempty"`
	Strategy     int              `json:"strategy,omitempty"`
	Timeout      int              `json:"timeout,omitempty"`
	SocksProxy   string           `json:"socks_proxy,omitempty"`
	BuiltInCache bool             `json:"built_in_cache,omitempty"`
	Upstreams    []*Upstream      `json:"upstreams,omitempty"`
	Bootstrap    []*Upstream      `json:"bootstrap,omitempty"`
	Blacklist    []string         `json:"blacklist,omitempty"`

	Debug     bool `json:"debug,omitempty"`
	Profiling bool `json:"profiling,omitempty"`

	// Connection pool settings
	MaxActiveConnections int `json:"max_active_connections,omitempty"` // Default: 50
	MaxIdleConnections   int `json:"max_idle_connections,omitempty"`   // Default: 20

	// Stats persistence interval in minutes
	StatsSaveInterval int `json:"stats_save_interval,omitempty"` // Default: 5 minutes

	BlacklistSplited [][]string `json:"-"`
}

func (c *Config) ReadInConfig(path string, ipRanger cidranger.Ranger, log logger.Logger) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(body), c); err != nil {
		return err
	}

	// Set default connection pool values
	if c.MaxActiveConnections == 0 {
		c.MaxActiveConnections = 50
	}
	if c.MaxIdleConnections == 0 {
		c.MaxIdleConnections = 20
	}

	// Set default stats save interval (5 minutes)
	if c.StatsSaveInterval == 0 {
		c.StatsSaveInterval = 5
	}

	for i := 0; i < len(c.Bootstrap); i++ {
		c.Bootstrap[i].Init(c, ipRanger, log)
		if net.ParseIP(c.Bootstrap[i].host) == nil {
			return errors.New("Bootstrap 服务器只能使用 IP: " + c.Bootstrap[i].Address)
		}
		c.Bootstrap[i].InitConnectionPool(nil)
	}
	for i := 0; i < len(c.Upstreams); i++ {
		c.Upstreams[i].Init(c, ipRanger, log)
		if err := c.Upstreams[i].Validate(); err != nil {
			return err
		}
	}
	c.BlacklistSplited = utils.ParseRules(c.Blacklist)
	return nil
}

func (c *Config) GetDialerContext(d *net.Dialer) (proxy.Dialer, proxy.ContextDialer, error) {
	dialSocksProxy, err := proxy.SOCKS5("tcp", c.SocksProxy, nil, d)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Error creating SOCKS5 proxy")
	}
	if dialContext, ok := dialSocksProxy.(proxy.ContextDialer); !ok {
		return nil, nil, errors.New("Failed type assertion to DialContext")
	} else {
		return dialSocksProxy, dialContext, err
	}
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
