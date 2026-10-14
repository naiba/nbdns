package model

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestControlledECSInheritance(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, want string
		family               uint16
		mask                 uint8
	}{
		{"inherit", `{}`, "180.152.0.0", 1, 24},
		{"override", `{"ecs":"2001:db8:abcd:1234::1/48"}`, "2001:db8:abcd::", 2, 48},
		{"disable", `{"ecs":""}`, "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			received := make(chan *dns.Msg, 1)
			server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) { received <- r.Copy(); _ = w.WriteMsg(new(dns.Msg).SetReply(r)) })}
			go func() { _ = server.ActivateAndServe() }()
			t.Cleanup(func() { _ = server.Shutdown() })
			var config Config
			if err := json.Unmarshal([]byte(`{"ecs":"180.152.0.123/24","timeout":2}`), &config); err != nil {
				t.Fatal(err)
			}
			var up Upstream
			if err := json.Unmarshal([]byte(tc.upstream), &up); err != nil {
				t.Fatal(err)
			}
			up.Address = "udp://" + conn.LocalAddr().String()
			up.Init(&config, logger.New(false))
			q := new(dns.Msg)
			q.SetQuestion("cdn.example.", dns.TypeA)
			q.SetEdns0(1232, true)
			q.IsEdns0().Option = append(q.IsEdns0().Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 32, Address: net.ParseIP("192.0.2.123").To4()}, &dns.EDNS0_NSID{Code: dns.EDNS0NSID})
			before := q.String()
			if _, _, err := up.Exchange(q); err != nil {
				t.Fatal(err)
			}
			got := <-received
			if q.String() != before {
				t.Fatal("Exchange mutated caller request")
			}
			opt := got.IsEdns0()
			if opt == nil || !opt.Do() || opt.UDPSize() != 1232 {
				t.Fatalf("EDNS metadata lost: %v", got)
			}
			count := 0
			nsid := false
			for _, option := range opt.Option {
				if s, ok := option.(*dns.EDNS0_SUBNET); ok {
					count++
					if s.Address.String() != tc.want || s.Family != tc.family || s.SourceNetmask != tc.mask || s.SourceScope != 0 {
						t.Errorf("unexpected ECS: %+v", s)
					}
				}
				if _, ok := option.(*dns.EDNS0_NSID); ok {
					nsid = true
				}
			}
			wantCount := 1
			if tc.want == "" {
				wantCount = 0
			}
			if count != wantCount || !nsid {
				t.Errorf("ECS count=%d want=%d NSID=%v", count, wantCount, nsid)
			}
		})
	}
}
