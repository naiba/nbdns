package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/naiba/nbdns/internal/model"
)

// Diagnosis explains how nbdns resolves one name, for the dashboard. It asks
// every matching upstream directly instead of calling HandleDnsMsg, so it
// neither counts in statistics nor fills the cache.
type Diagnosis struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Blocked bool              `json:"blocked"` // A filter rule answers NXDOMAIN
	Route   string            `json:"route"`   // DiagnosisRouteDedicated or DiagnosisRouteDefault
	Sources []DiagnosisSource `json:"sources"`
}

// DiagnosisSource is one place an answer can come from, in serving precedence.
type DiagnosisSource struct {
	Kind      string   `json:"kind"` // "custom", "cache" or "upstream"
	Address   string   `json:"address,omitempty"`
	Rcode     string   `json:"rcode,omitempty"`
	Answers   []string `json:"answers,omitempty"`
	LatencyMs float64  `json:"latency_ms,omitempty"`
	// TTLSeconds is the remaining cache lifetime; negative when expired (served
	// only as a fallback while upstreams fail).
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	Error      string `json:"error,omitempty"`
	// RebindBlocked: dnsmasq rebind protection would turn this answer into an
	// empty reply (see model.AnswerHasDnsmasqRebindAddress).
	RebindBlocked bool `json:"rebind_blocked"`
}

const (
	DiagnosisRouteDedicated = "dedicated" // Matched match / match_lists of an upstream
	DiagnosisRouteDefault   = "default"
)

const diagnosisTimeout = 5 * time.Second

// ErrInvalidDiagnosisName rejects names that are not valid DNS names.
var ErrInvalidDiagnosisName = errors.New("invalid domain name")

// Diagnose resolves name with qtype through every route nbdns would consider.
func (h *Handler) Diagnose(ctx context.Context, name string, qtype uint16) (Diagnosis, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "." {
		return Diagnosis{}, ErrInvalidDiagnosisName
	}
	fqdn := dns.Fqdn(name)
	if _, ok := dns.IsDomainName(fqdn); !ok {
		return Diagnosis{}, ErrInvalidDiagnosisName
	}
	req := new(dns.Msg).SetQuestion(fqdn, qtype)
	result := Diagnosis{Name: fqdn, Type: dns.TypeToString[qtype], Route: DiagnosisRouteDefault, Sources: []DiagnosisSource{}}
	if result.Type == "" {
		result.Type = dns.Type(qtype).String()
	}

	if records, ok := h.customRecords[fqdn]; ok {
		resp := new(dns.Msg).SetReply(req)
		for _, rr := range records {
			if qtype == dns.TypeANY || rr.Header().Rrtype == qtype || rr.Header().Rrtype == dns.TypeCNAME {
				resp.Answer = append(resp.Answer, rr)
			}
		}
		result.Sources = append(result.Sources, diagnosisSource("custom", "", resp))
	}
	result.Blocked = h.filter != nil && h.filter.Blocked(fqdn)

	if h.builtInCache != nil {
		if cached, ok := h.builtInCache.Get(namespacedDNSCacheKey(h.cacheNamespace, req)); ok {
			source := diagnosisSource("cache", "", cached.Msg)
			// Round away from zero so an entry expiring this second is not shown as "no TTL".
			remaining := time.Until(cached.Expires)
			source.TTLSeconds = int(remaining / time.Second)
			if remaining < 0 && source.TTLSeconds == 0 {
				source.TTLSeconds = -1
			}
			result.Sources = append(result.Sources, source)
		}
	}

	upstreams := h.matchedUpstreams(req)
	for _, up := range h.specialUpstreams {
		if up.IsMatch(fqdn) {
			result.Route = DiagnosisRouteDedicated
			break
		}
	}
	ctx, cancel := context.WithTimeout(ctx, diagnosisTimeout)
	defer cancel()
	answers := make([]DiagnosisSource, len(upstreams))
	var wg sync.WaitGroup
	for i, up := range upstreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = diagnoseUpstream(ctx, up, req.Copy())
		}()
	}
	wg.Wait()
	result.Sources = append(result.Sources, answers...)
	return result, nil
}

func diagnoseUpstream(ctx context.Context, up *model.Upstream, req *dns.Msg) DiagnosisSource {
	started := time.Now()
	resp, _, err := up.ExchangeContext(ctx, req)
	source := DiagnosisSource{Kind: "upstream", Address: up.Address}
	if err == nil {
		source = diagnosisSource("upstream", up.Address, resp)
	} else {
		source.Error = err.Error()
	}
	source.LatencyMs = float64(time.Since(started).Microseconds()) / 1000
	return source
}

func diagnosisSource(kind, address string, resp *dns.Msg) DiagnosisSource {
	source := DiagnosisSource{Kind: kind, Address: address, Answers: []string{}}
	if resp == nil {
		return source
	}
	source.Rcode = dns.RcodeToString[resp.Rcode]
	for _, rr := range resp.Answer {
		header := rr.Header()
		data := strings.TrimSpace(strings.TrimPrefix(rr.String(), header.String()))
		source.Answers = append(source.Answers, dns.Type(header.Rrtype).String()+" "+data)
	}
	source.RebindBlocked = model.AnswerHasDnsmasqRebindAddress(resp)
	return source
}
