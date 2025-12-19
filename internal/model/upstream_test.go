package model

import (
	"index/suffixarray"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
	"github.com/naiba/nbdns/pkg/utils"
	"github.com/yl2chen/cidranger"
)

var primaryLocations = []string{"中国", "省", "市", "自治区"}
var nonPrimaryLocations = []string{"台湾", "香港", "澳门"}

var primaryLocationsBytes = [][]byte{[]byte("中国"), []byte("省"), []byte("市"), []byte("自治区")}
var nonPrimaryLocationsBytes = [][]byte{[]byte("台湾"), []byte("香港"), []byte("澳门")}

func BenchmarkCheckPrimary(b *testing.B) {
	for i := 0; i < b.N; i++ {
		checkPrimary("哈哈")
	}
}

func BenchmarkCheckPrimaryStringsContains(b *testing.B) {
	for i := 0; i < b.N; i++ {
		checkPrimaryStringsContains("哈哈")
	}
}

func TestIsMatch(t *testing.T) {
	var up Upstream
	up.matchSplited = utils.ParseRules([]string{"."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     true,
		".b.a.com.cn.": true,
		"b.a.com.cn.":  true,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{""})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{".a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     true,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"b.d.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		".a.com.":      false,
		"b.d.com.":     true,
		".b.d.com.cn.": false,
		"b.d.com.cn.":  false,
		".c.d.com.":    false,
		"b.d.a.com.":   false,
	}, t)
}

func checkUpstreamMatch(up *Upstream, cases map[string]bool, t *testing.T) {
	for k, v := range cases {
		isMatch := up.IsMatch(k)
		if isMatch != v {
			t.Errorf("Upstream(%s).IsMatch(%s) = %v, want %v", up.matchSplited, k, isMatch, v)
		}
	}
}

func checkPrimary(str string) bool {
	index := suffixarray.New([]byte(str))
	for i := 0; i < len(nonPrimaryLocationsBytes); i++ {
		if len(index.Lookup(nonPrimaryLocationsBytes[i], 1)) > 0 {
			return false
		}
	}
	for i := 0; i < len(primaryLocationsBytes); i++ {
		if len(index.Lookup(primaryLocationsBytes[i], 1)) > 0 {
			return true
		}
	}
	return false
}

func checkPrimaryStringsContains(str string) bool {
	for i := 0; i < len(nonPrimaryLocations); i++ {
		if strings.Contains(str, nonPrimaryLocations[i]) {
			return false
		}
	}
	for i := 0; i < len(primaryLocations); i++ {
		if strings.Contains(str, primaryLocations[i]) {
			return true
		}
	}
	return false
}

// TestIsPrivateIP tests the isPrivateIP function
func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		expected bool
	}{
		// Private IPv4 ranges
		{"10.0.0.0", "10.0.0.0", true},
		{"10.255.255.255", "10.255.255.255", true},
		{"10.1.2.3", "10.1.2.3", true},
		{"172.16.0.0", "172.16.0.0", true},
		{"172.31.255.255", "172.31.255.255", true},
		{"172.20.1.1", "172.20.1.1", true},
		{"192.168.0.0", "192.168.0.0", true},
		{"192.168.255.255", "192.168.255.255", true},
		{"192.168.1.1", "192.168.1.1", true},

		// Public IPv4 addresses (not private)
		{"8.8.8.8", "8.8.8.8", false},
		{"1.1.1.1", "1.1.1.1", false},
		{"172.15.0.1", "172.15.0.1", false},   // Just before 172.16.0.0/12
		{"172.32.0.1", "172.32.0.1", false},   // Just after 172.31.255.255
		{"192.167.1.1", "192.167.1.1", false}, // Not 192.168
		{"192.169.1.1", "192.169.1.1", false}, // Not 192.168
		{"11.0.0.1", "11.0.0.1", false},       // Not 10.x.x.x

		// IPv6 private (Unique Local Addresses fc00::/7)
		{"fc00::1", "fc00::1", true},
		{"fd00::1", "fd00::1", true},
		{"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},

		// IPv6 public (not private)
		{"2001:4860:4860::8888", "2001:4860:4860::8888", false},
		{"fe80::1", "fe80::1", false}, // Link-local, not ULA

		// Edge cases
		{"nil IP", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ip net.IP
			if tt.ip != "" {
				ip = net.ParseIP(tt.ip)
			}
			result := isPrivateIP(ip)
			if result != tt.expected {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}

// TestIsValidMsgWithPrivateIP tests that private IPs are not dropped
func TestIsValidMsgWithPrivateIP(t *testing.T) {
	// Create a simple IP ranger with a test IP range (e.g., 1.0.0.0/8)
	ipRanger := cidranger.NewPCTrieRanger()
	_, network, _ := net.ParseCIDR("1.0.0.0/8")
	ipRanger.Insert(cidranger.NewBasicRangerEntry(*network))

	log := logger.New(false)

	tests := []struct {
		name       string
		isPrimary  bool
		ip         string
		shouldPass bool
		reason     string
	}{
		{
			name:       "Primary DNS with private IP 10.x",
			isPrimary:  true,
			ip:         "10.0.0.1",
			shouldPass: true,
			reason:     "Private IPs should always be valid",
		},
		{
			name:       "Primary DNS with private IP 172.16.x",
			isPrimary:  true,
			ip:         "172.16.0.1",
			shouldPass: true,
			reason:     "Private IPs should always be valid",
		},
		{
			name:       "Primary DNS with private IP 192.168.x",
			isPrimary:  true,
			ip:         "192.168.1.1",
			shouldPass: true,
			reason:     "Private IPs should always be valid",
		},
		{
			name:       "Primary DNS with public non-primary IP",
			isPrimary:  true,
			ip:         "8.8.8.8",
			shouldPass: false,
			reason:     "Public non-primary IPs should be rejected by primary DNS",
		},
		{
			name:       "Primary DNS with public primary IP",
			isPrimary:  true,
			ip:         "1.0.0.1",
			shouldPass: true,
			reason:     "Primary IPs should be valid for primary DNS",
		},
		{
			name:       "Non-primary DNS with private IP",
			isPrimary:  false,
			ip:         "10.0.0.1",
			shouldPass: true,
			reason:     "Private IPs should always be valid",
		},
		{
			name:       "Non-primary DNS with public IP",
			isPrimary:  false,
			ip:         "8.8.8.8",
			shouldPass: true,
			reason:     "Non-primary DNS accepts any IP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				BlacklistSplited: [][]string{},
			}

			upstream := &Upstream{
				IsPrimary: tt.isPrimary,
				Address:   "test://example.com:53",
				config:    config,
				ipRanger:  ipRanger,
				logger:    log,
			}

			// Create a DNS message with an A record
			msg := &dns.Msg{
				Answer: []dns.RR{
					&dns.A{
						Hdr: dns.RR_Header{
							Name:   "example.com.",
							Rrtype: dns.TypeA,
							Class:  dns.ClassINET,
							Ttl:    300,
						},
						A: net.ParseIP(tt.ip).To4(),
					},
				},
			}

			result := upstream.IsValidMsg(msg)
			if result != tt.shouldPass {
				t.Errorf("%s: IsValidMsg() = %v, want %v. Reason: %s", tt.name, result, tt.shouldPass, tt.reason)
			}
		})
	}
}
