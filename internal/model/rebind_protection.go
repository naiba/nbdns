package model

import (
	"net"
	"net/netip"

	"github.com/miekg/dns"
)

// dnsmasqRebindPrefixes mirrors dnsmasq private_net()/private_net6() with
// ban_localhost, i.e. OpenWrt rebind_protection with the default
// rebind_localhost=0. dnsmasq discards upstream answers containing these
// addresses, so its clients get an empty reply although nbdns resolved the name.
// Keep this list identical to dnsmasq instead of reusing ecsBlackholePrefixes:
// that list answers a different question (198.18/15 and multicast are
// blackholes there, yet dnsmasq passes them; documentation ranges are the reverse).
var dnsmasqRebindPrefixes = func() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.2.0/24", "192.168.0.0/16", "198.51.100.0/24",
		"203.0.113.0/24", "255.255.255.255/32",
		// dnsmasq checks only fd00::/8 of fc00::/7, plus deprecated site-local fec0::/10.
		"::/128", "::1/128", "fe80::/10", "fec0::/10", "fd00::/8", "2001:db8::/32",
	} {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}()

// IsDnsmasqRebindAddress reports whether dnsmasq rebind protection drops an
// answer containing ip. IPv4-mapped IPv6 addresses are judged as IPv4, as dnsmasq does.
func IsDnsmasqRebindAddress(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range dnsmasqRebindPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// AnswerHasDnsmasqRebindAddress reports whether any A/AAAA in the answer
// section would make dnsmasq rebind protection turn the reply into an empty answer.
func AnswerHasDnsmasqRebindAddress(msg *dns.Msg) bool {
	if msg == nil {
		return false
	}
	// Runs on every served reply: walk the records instead of allocating answerIPs.
	for _, rr := range msg.Answer {
		switch record := rr.(type) {
		case *dns.A:
			if IsDnsmasqRebindAddress(record.A) {
				return true
			}
		case *dns.AAAA:
			if IsDnsmasqRebindAddress(record.AAAA) {
				return true
			}
		}
	}
	return false
}
