package model

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/naiba/nbdns/pkg/logger"
	"github.com/pkg/errors"
	"golang.org/x/net/proxy"
)

type DohServerConfig struct {
	Username string `json:"username,omitempty"` // DoH Basic Auth 用户名（可选）
	Password string `json:"password,omitempty"` // DoH Basic Auth 密码（可选）
}

type Config struct {
	CustomRecords             []CustomRecord   `json:"custom_records,omitempty"`
	ServeAddr                 string           `json:"serve_addr,omitempty"`
	WebAddr                   string           `json:"web_addr,omitempty"`
	DohServer                 *DohServerConfig `json:"doh_server,omitempty"`
	ECS                       string           `json:"ecs,omitempty"`
	Timeout                   int              `json:"timeout,omitempty"`
	SocksProxy                string           `json:"socks_proxy,omitempty"`
	BuiltInCache              bool             `json:"built_in_cache,omitempty"`
	Upstreams                 []*Upstream      `json:"upstreams,omitempty"`
	Bootstrap                 []*Upstream      `json:"bootstrap,omitempty"`
	FilterLists               []string         `json:"filter_lists,omitempty"`                 // AdGuard DNS / hosts files, relative to data/
	FilterUpdateIntervalHours int              `json:"filter_update_interval_hours,omitempty"` // Default 24; negative disables periodic updates

	Debug     bool `json:"debug,omitempty"`
	Profiling bool `json:"profiling,omitempty"`

	// Connection pool settings
	MaxActiveConnections int `json:"max_active_connections,omitempty"` // Default: 50
	MaxIdleConnections   int `json:"max_idle_connections,omitempty"`   // Default: 20

	// Stats persistence interval in minutes
	StatsSaveInterval int `json:"stats_save_interval,omitempty"` // Default: 5 minutes

}

func (c *Config) ReadInConfig(path string, log logger.Logger) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(body), c); err != nil {
		return err
	}

	if _, err := CompileCustomRecords(c.CustomRecords); err != nil {
		return err
	}
	if _, err := parseECS(c.ECS); err != nil {
		return err
	}
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	if c.Timeout <= 0 {
		c.Timeout = 4
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
	if c.FilterUpdateIntervalHours == 0 {
		c.FilterUpdateIntervalHours = 24
	}

	for i := 0; i < len(c.Bootstrap); i++ {
		if c.Bootstrap[i] == nil {
			return fmt.Errorf("bootstrap entry must not be null")
		}
		c.Bootstrap[i].Init(c, log)
		if net.ParseIP(c.Bootstrap[i].host) == nil {
			return errors.New("Bootstrap 服务器只能使用 IP: " + c.Bootstrap[i].Address)
		}
		if err := c.Bootstrap[i].Validate(); err != nil {
			return err
		}
		c.Bootstrap[i].InitConnectionPool(nil)
	}
	for i := 0; i < len(c.Upstreams); i++ {
		if c.Upstreams[i] == nil {
			return fmt.Errorf("upstream entry must not be null")
		}
		c.Upstreams[i].Init(c, log)
		if err := c.Upstreams[i].Validate(); err != nil {
			return err
		}
	}
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
