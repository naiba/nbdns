package filter

import (
	"bufio"
	"io"
	"strings"
)

// parseDomainList reads domain lists used to route names to upstreams, e.g.
// dnsmasq-china-list ("server=/example.com/114.114.114.114"), plain domain
// lists, v2ray-style "full:"/"domain:" and Clash "DOMAIN-SUFFIX," entries.
// Entries match the domain and its subdomains unless marked exact. With a nil
// filter the input is only validated.
func parseDomainList(reader io.Reader, f *Filter) (Stats, error) {
	var stats Stats
	add := func(domain string, exact bool) bool {
		domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
		if !validDomain(domain) {
			return false
		}
		if f != nil {
			f.addDomain(domain, exact)
		}
		stats.Rules++
		return true
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		supported := false
		switch {
		case strings.HasPrefix(line, "server=/"), strings.HasPrefix(line, "nftset=/"),
			strings.HasPrefix(line, "ipset=/"), strings.HasPrefix(line, "address=/"):
			// dnsmasq: /domain1/domain2/.../value; the last field is never a domain.
			fields := strings.Split(line[strings.IndexByte(line, '/')+1:], "/")
			for _, domain := range fields[:max(len(fields)-1, 0)] {
				if add(domain, false) {
					supported = true
				}
			}
		case strings.HasPrefix(line, "full:"):
			supported = add(strings.TrimPrefix(line, "full:"), true)
		case strings.HasPrefix(line, "domain:"):
			supported = add(strings.TrimPrefix(line, "domain:"), false)
		case strings.HasPrefix(line, "DOMAIN-SUFFIX,"):
			domain, _, _ := strings.Cut(strings.TrimPrefix(line, "DOMAIN-SUFFIX,"), ",")
			supported = add(domain, false)
		case strings.HasPrefix(line, "DOMAIN,"):
			domain, _, _ := strings.Cut(strings.TrimPrefix(line, "DOMAIN,"), ",")
			supported = add(domain, true)
		case strings.ContainsAny(line, " \t:,=/*"):
			// regexp:, keyword:, wildcards and other syntaxes are not supported.
		default:
			supported = add(strings.TrimPrefix(strings.TrimPrefix(line, "+"), "."), false)
		}
		if !supported {
			stats.Unsupported++
		}
	}
	return stats, scanner.Err()
}

// parseFilterRules adapts the AdGuard/hosts filter syntax to the same shape.
func parseFilterRules(reader io.Reader, f *Filter) (Stats, error) {
	if f == nil {
		return parseRules(reader, nil)
	}
	return parseRules(reader, f.add)
}
