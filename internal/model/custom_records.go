package model

import (
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

// CustomRecord describes a local DNS override. Value uses DNS presentation syntax.
type CustomRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   uint32 `json:"ttl"`
}

func CompileCustomRecords(records []CustomRecord) (map[string][]dns.RR, error) {
	result := make(map[string][]dns.RR)
	for i, record := range records {
		name := strings.ToLower(dns.Fqdn(record.Name))
		if _, valid := dns.IsDomainName(name); !valid || record.Name == "" || strings.ContainsAny(record.Name, "* \t\r\n") {
			return nil, fmt.Errorf("custom_records[%d]: invalid exact domain name", i)
		}
		kind := strings.ToUpper(record.Type)
		switch kind {
		case "A", "AAAA", "CNAME", "TXT", "MX", "SRV", "PTR", "NS":
		default:
			return nil, fmt.Errorf("custom_records[%d]: unsupported record type %q", i, record.Type)
		}
		if strings.ContainsAny(record.Value, "\r\n") {
			return nil, fmt.Errorf("custom_records[%d]: value must be a single line", i)
		}
		rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", name, record.TTL, kind, record.Value))
		if err != nil {
			return nil, fmt.Errorf("custom_records[%d]: %w", i, err)
		}
		for _, existing := range result[name] {
			// CNAME cannot coexist with other records at the same owner name.
			if existing.Header().Rrtype == dns.TypeCNAME || rr.Header().Rrtype == dns.TypeCNAME {
				return nil, fmt.Errorf("custom_records[%d]: CNAME must be the only record for %s", i, name)
			}
		}
		result[name] = append(result[name], rr)
	}
	return result, nil
}
