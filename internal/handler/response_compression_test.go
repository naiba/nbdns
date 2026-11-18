package handler

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/logger"
)

// A CDN answer with 2 CNAMEs + 8 A records exceeds 512 bytes uncompressed but
// is less than half of that compressed. dnsmasq forwards with EDNS 1232 and relays the reply to
// non-EDNS clients, where an uncompressed reply exceeds 512 bytes and becomes
// an empty TC answer. Every UDP reply must therefore be name-compressed.
func TestUDPRepliesAreNameCompressed(t *testing.T) {
	upstream, _ := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		resp := new(dns.Msg).SetReply(q)
		resp.Answer = append(resp.Answer,
			&dns.CNAME{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "storefront-cdn-primary.alias.example."},
			&dns.CNAME{Hdr: dns.RR_Header{Name: "storefront-cdn-primary.alias.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "storefront.example.edge-gslb.example."})
		for i := 0; i < 8; i++ {
			resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: "storefront.example.edge-gslb.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(198, 51, 100, byte(204+i))})
		}
		return resp
	})
	h := NewHandler(false, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(h.HandleRequest)}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })

	query := new(dns.Msg).SetQuestion("storefront.example.", dns.TypeA)
	query.SetEdns0(1232, false) // What dnsmasq sends upstream
	wire, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write(wire); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	reply := new(dns.Msg)
	if err := reply.Unpack(buf[:n]); err != nil {
		t.Fatal(err)
	}
	if reply.Truncated || len(reply.Answer) != 10 {
		t.Fatalf("reply truncated=%v answers=%d", reply.Truncated, len(reply.Answer))
	}
	if n > dns.MinMsgSize {
		t.Errorf("reply is %d bytes; uncompressed replies over 512 bytes become empty TC answers behind dnsmasq", n)
	}
}
