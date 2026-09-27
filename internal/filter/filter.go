// Package filter implements the domain-only subset of AdGuard DNS filters.
// A filter is populated once at startup and is then safe for concurrent reads.
package filter

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Stats struct {
	Rules       int
	Unsupported int
}

type node struct {
	children map[string]*node
	blocked  bool
	allowed  bool
}

type Filter struct {
	root node
}

type Matcher interface {
	Blocked(name string) bool
}

func New() *Filter { return &Filter{} }

// Blocked matches complete DNS labels, including subdomains. An exception
// anywhere in the suffix chain takes precedence over a blocking rule.
func (f *Filter) Blocked(name string) bool {
	if f == nil {
		return false
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if !validDomain(name) {
		return false
	}
	n := &f.root
	var blocked, allowed bool
	for {
		index := strings.LastIndexByte(name, '.')
		label := name[index+1:]
		n = n.children[label]
		if n == nil {
			break
		}
		blocked = blocked || n.blocked
		allowed = allowed || n.allowed
		if index < 0 {
			break
		}
		name = name[:index]
	}
	return blocked && !allowed
}

func (f *Filter) add(domain string, allow bool) {
	n := &f.root
	for {
		index := strings.LastIndexByte(domain, '.')
		label := domain[index+1:]
		if n.children == nil {
			n.children = make(map[string]*node)
		}
		if n.children[label] == nil {
			n.children[label] = &node{}
		}
		n = n.children[label]
		if index < 0 {
			break
		}
		domain = domain[:index]
	}
	if allow {
		n.allowed = true
	} else {
		n.blocked = true
	}
}

func validDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	labelLen := 0
	for i := 0; i < len(domain); i++ {
		c := domain[i]
		if c == '.' {
			if labelLen == 0 || domain[i-1] == '-' {
				return false
			}
			labelLen = 0
			continue
		}
		if labelLen == 0 && c == '-' || labelLen >= 63 || !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
		labelLen++
	}
	return labelLen > 0 && domain[len(domain)-1] != '-'
}

// Load supports ||domain^, @@||domain^, and hosts-file mappings to loopback
// or the zero address. All other non-comment rules are counted as unsupported.
func (f *Filter) Load(reader io.Reader) (Stats, error) {
	return parseRules(reader, f.add)
}

// Validate checks the entire input without allocating a temporary domain
// index. It must finish before an update can replace a last-known-good list.
func Validate(reader io.Reader, requireRules bool) (Stats, error) {
	stats, err := parseRules(reader, nil)
	if err == nil && requireRules && stats.Rules == 0 {
		return stats, fmt.Errorf("subscription has no supported rules")
	}
	return stats, err
}

func parseRules(reader io.Reader, add func(domain string, allow bool)) (Stats, error) {
	var stats Stats
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "[") {
			continue
		}
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		allow := strings.HasPrefix(line, "@@")
		if allow {
			line = strings.TrimPrefix(line, "@@")
		}
		if strings.HasPrefix(line, "||") && strings.HasSuffix(line, "^") {
			domain := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(line, "||"), "^"))
			if validDomain(domain) {
				if add != nil {
					add(domain, allow)
				}
				stats.Rules++
				continue
			}
		} else if !allow {
			fields := strings.Fields(line)
			if len(fields) >= 2 && isBlockingIP(fields[0]) {
				valid := true
				for _, domain := range fields[1:] {
					if !validDomain(strings.ToLower(domain)) {
						valid = false
						break
					}
				}
				if valid {
					for _, domain := range fields[1:] {
						if domain == "localhost" || domain == "localhost.localdomain" {
							continue
						}
						if add != nil {
							add(strings.ToLower(domain), false)
						}
						stats.Rules++
					}
					continue
				}
			}
		}
		stats.Unsupported++
	}
	return stats, scanner.Err()
}

func isBlockingIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && (ip.IsLoopback() || ip.Equal(net.IPv4zero) || ip.Equal(net.IPv6zero))
}

func LoadFiles(dataPath string, files []string) (*Filter, Stats, error) {
	f := New()
	var total Stats
	for _, path := range files {
		if !filepath.IsAbs(path) {
			path = filepath.Join(dataPath, path)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, total, fmt.Errorf("open filter list %q: %w", path, err)
		}
		stats, loadErr := f.Load(file)
		closeErr := file.Close()
		if loadErr != nil {
			return nil, total, fmt.Errorf("load filter list %q: %w", path, loadErr)
		}
		if closeErr != nil {
			return nil, total, closeErr
		}
		total.Rules += stats.Rules
		total.Unsupported += stats.Unsupported
	}
	return f, total, nil
}
