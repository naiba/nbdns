package model

import "testing"

func TestCustomRecordValidation(t *testing.T) {
	for _, record := range []CustomRecord{
		{Name: "*.example", Type: "A", Value: "192.0.2.1"},
		{Name: "example", Type: "A", Value: "not-an-ip"},
		{Name: "example", Type: "AAAA", Value: "192.0.2.1"},
		{Name: "example", Type: "UNKNOWN", Value: "value"},
		{Name: "example", Type: "TXT", Value: "\"hello\"\nother.example. IN A 192.0.2.1"},
	} {
		if _, err := CompileCustomRecords([]CustomRecord{record}); err == nil {
			t.Fatalf("accepted invalid record: %+v", record)
		}
	}
	if _, err := CompileCustomRecords([]CustomRecord{{Name: "example", Type: "CNAME", Value: "target.example."}, {Name: "example", Type: "A", Value: "192.0.2.1"}}); err == nil {
		t.Fatal("accepted CNAME conflict")
	}
	for _, record := range []CustomRecord{
		{Name: "example", Type: "TXT", Value: "\"hello world\""},
		{Name: "example", Type: "MX", Value: "10 mail.example."},
		{Name: "_dns._tcp.example", Type: "SRV", Value: "0 10 53 dns.example."},
		{Name: "1.2.0.192.in-addr.arpa", Type: "PTR", Value: "host.example."},
		{Name: "example", Type: "NS", Value: "ns.example."},
	} {
		if _, err := CompileCustomRecords([]CustomRecord{record}); err != nil {
			t.Fatal(err)
		}
	}
}
