// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"

	"github.com/miekg/dns"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
)

type isoDNSPoolStore interface {
	ISOPool(context.Context) (netip.Prefix, error)
}

type isoDNSHandler struct {
	store   isoDNSPoolStore
	forward dnsForwardFunc
}

func newISODNSHandler(store isoDNSPoolStore, forward dnsForwardFunc) dns.Handler {
	if forward == nil {
		forward = forwardISODNSViaHostResolver
	}
	return &isoDNSHandler{store: store, forward: forward}
}

func (h *isoDNSHandler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := h.responseFor(req, w.RemoteAddr())
	if err := w.WriteMsg(resp); err != nil {
		log.Printf("failed to write ISO DNS response: %v", err)
	}
}

func (h *isoDNSHandler) responseFor(req *dns.Msg, remote net.Addr) *dns.Msg {
	if req == nil || len(req.Question) != 1 {
		return newISODNSRcode(req, dns.RcodeFormatError)
	}
	pool, err := h.loadPool()
	if err != nil {
		return newISODNSRcode(req, dns.RcodeServerFailure)
	}
	if !isoDNSSourceAllowed(remote, pool) {
		return newISODNSRcode(req, dns.RcodeRefused)
	}
	q := req.Question[0]
	if isISODNSRefusedName(q.Name, pool) {
		return newISODNSRcode(req, dns.RcodeRefused)
	}
	return h.forwardResponse(req, pool)
}

func (h *isoDNSHandler) loadPool() (netip.Prefix, error) {
	if h.store == nil {
		return netip.Prefix{}, fmt.Errorf("ISO DNS pool store is unavailable")
	}
	pool, err := h.store.ISOPool(context.Background())
	if err != nil {
		return netip.Prefix{}, err
	}
	if !validISODNSPool(pool) {
		return netip.Prefix{}, fmt.Errorf("ISO DNS pool is invalid")
	}
	return pool, nil
}

func isISODNSRefusedName(name string, pool netip.Prefix) bool {
	return isISODNSYeetLocalName(name) || isShortYeetDNSCandidate(name) || isISOReverseName(name, pool) ||
		isReverseNameForPrefix(name, netip.MustParsePrefix(netns.ServiceSubnetCIDR))
}

func (h *isoDNSHandler) forwardResponse(req *dns.Msg, pool netip.Prefix) *dns.Msg {
	resp, err := h.forward(context.Background(), req.Copy())
	if err != nil || resp == nil {
		return newISODNSRcode(req, dns.RcodeServerFailure)
	}
	return filterISODNSMessage(resp, pool)
}

func newISODNSRcode(req *dns.Msg, rcode int) *dns.Msg {
	if req == nil {
		req = new(dns.Msg)
	}
	resp := new(dns.Msg)
	resp.SetRcode(req, rcode)
	return resp
}

func validISODNSPool(pool netip.Prefix) bool {
	return pool.IsValid() && pool.Addr().Is4() && pool.Bits() == 16 && pool == pool.Masked()
}

func isoDNSSourceAllowed(remote net.Addr, pool netip.Prefix) bool {
	addr, ok := isoDNSRemoteAddr(remote)
	return ok && pool.Contains(addr)
}

func isoDNSRemoteAddr(remote net.Addr) (netip.Addr, bool) {
	if remote == nil {
		return netip.Addr{}, false
	}
	var ip net.IP
	switch addr := remote.(type) {
	case *net.UDPAddr:
		ip = addr.IP
	case *net.TCPAddr:
		ip = addr.IP
	default:
		host, _, err := net.SplitHostPort(remote.String())
		if err != nil {
			return netip.Addr{}, false
		}
		parsed, err := netip.ParseAddr(strings.Trim(host, "[]"))
		if err != nil {
			return netip.Addr{}, false
		}
		parsed = parsed.Unmap()
		return parsed, parsed.Is4()
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	addr = addr.Unmap()
	return addr, addr.Is4()
}

func isISODNSYeetLocalName(qname string) bool {
	name := strings.ToLower(dns.Fqdn(strings.TrimSpace(qname)))
	return name == yeetDNSDomain || strings.HasSuffix(name, "."+yeetDNSDomain)
}

func isISOReverseName(qname string, pool netip.Prefix) bool {
	return validISODNSPool(pool) && isReverseNameForPrefix(qname, pool)
}

func isReverseNameForPrefix(qname string, prefix netip.Prefix) bool {
	prefix = prefix.Masked()
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits()%8 != 0 {
		return false
	}
	octets := prefix.Addr().As4()
	labels := make([]string, 0, prefix.Bits()/8)
	for index := prefix.Bits()/8 - 1; index >= 0; index-- {
		labels = append(labels, fmt.Sprint(octets[index]))
	}
	zone := strings.Join(labels, ".") + ".in-addr.arpa."
	name := strings.ToLower(dns.Fqdn(strings.TrimSpace(qname)))
	return name == zone || strings.HasSuffix(name, "."+zone)
}

func filterISODNSMessage(msg *dns.Msg, pool netip.Prefix) *dns.Msg {
	if msg == nil {
		return new(dns.Msg)
	}
	out := msg.Copy()
	out.Answer = filterISODNSRecords(msg.Answer, pool)
	out.Ns = filterISODNSRecords(msg.Ns, pool)
	out.Extra = filterISODNSRecords(msg.Extra, pool)
	return out
}

func filterISODNSRecords(records []dns.RR, pool netip.Prefix) []dns.RR {
	out := make([]dns.RR, 0, len(records))
	for _, original := range records {
		if original == nil {
			continue
		}
		cloned := dns.Copy(original)
		switch rr := cloned.(type) {
		case *dns.A:
			addr, ok := netip.AddrFromSlice(rr.A.To4())
			if !ok || !iso.IsPublicIPv4(addr.Unmap(), pool) {
				continue
			}
		case *dns.AAAA:
			continue
		case *dns.SVCB:
			rr.Value = filterISODNSSVCBValues(rr.Value, pool)
		case *dns.HTTPS:
			rr.Value = filterISODNSSVCBValues(rr.Value, pool)
		}
		out = append(out, cloned)
	}
	return out
}

func filterISODNSSVCBValues(values []dns.SVCBKeyValue, pool netip.Prefix) []dns.SVCBKeyValue {
	out := make([]dns.SVCBKeyValue, 0, len(values))
	retained := make(map[dns.SVCBKey]bool, len(values))
	for _, value := range values {
		switch hint := value.(type) {
		case *dns.SVCBIPv6Hint:
			continue
		case *dns.SVCBIPv4Hint:
			filtered := make([]net.IP, 0, len(hint.Hint))
			for _, raw := range hint.Hint {
				addr, ok := netip.AddrFromSlice(raw.To4())
				if ok && iso.IsPublicIPv4(addr.Unmap(), pool) {
					filtered = append(filtered, append(net.IP(nil), raw.To4()...))
				}
			}
			if len(filtered) == 0 {
				continue
			}
			hint.Hint = filtered
		}
		out = append(out, value)
		retained[value.Key()] = true
	}
	return filterISODNSMandatoryHints(out, retained)
}

func filterISODNSMandatoryHints(values []dns.SVCBKeyValue, retained map[dns.SVCBKey]bool) []dns.SVCBKeyValue {
	out := values[:0]
	for _, value := range values {
		mandatory, ok := value.(*dns.SVCBMandatory)
		if !ok {
			out = append(out, value)
			continue
		}
		codes := mandatory.Code[:0]
		for _, code := range mandatory.Code {
			if (code == dns.SVCB_IPV4HINT || code == dns.SVCB_IPV6HINT) && !retained[code] {
				continue
			}
			codes = append(codes, code)
		}
		if len(codes) == 0 {
			continue
		}
		mandatory.Code = codes
		out = append(out, mandatory)
	}
	return out
}

func forwardISODNSViaHostResolver(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, fmt.Errorf("read host resolv.conf: %w", err)
	}
	udpClient := &dns.Client{Net: "udp", ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout}
	tcpClient := &dns.Client{Net: "tcp", ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout}
	exchange := exchangeISODNSWithTCPFallback(exchangeDNSWithClient(udpClient), exchangeDNSWithClient(tcpClient))
	return forwardISODNSViaResolverConfig(ctx, req, cfg, exchange)
}

func exchangeISODNSWithTCPFallback(udp, tcp dnsExchangeFunc) dnsExchangeFunc {
	return func(ctx context.Context, req *dns.Msg, addr string) (*dns.Msg, error) {
		resp, err := udp(ctx, req, addr)
		if err != nil || resp == nil || !resp.Truncated {
			return resp, err
		}
		return tcp(ctx, req.Copy(), addr)
	}
}

func forwardISODNSViaResolverConfig(ctx context.Context, req *dns.Msg, cfg *dns.ClientConfig, exchange dnsExchangeFunc) (*dns.Msg, error) {
	if cfg == nil {
		return nil, fmt.Errorf("host resolver config is nil")
	}
	port := cfg.Port
	if port == "" {
		port = "53"
	}
	if port == "5353" {
		return nil, fmt.Errorf("no usable ordinary upstream DNS servers after excluding ISO DNS listener port")
	}
	servers := usableISOHostResolverServers(cfg.Servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no usable ordinary upstream DNS servers after excluding Yeet and Tailscale resolvers")
	}
	return forwardDNSViaServers(ctx, req, servers, port, exchange)
}

func usableISOHostResolverServers(servers []string) []string {
	out := make([]string, 0, len(servers))
	for _, server := range servers {
		server = strings.Trim(strings.TrimSpace(server), "[]")
		addr, err := netip.ParseAddr(server)
		if err != nil || addr.IsUnspecified() {
			continue
		}
		addr = addr.Unmap()
		if addr == netip.MustParseAddr(yeetDNSHostIP) || addr == netip.MustParseAddr(tailscaleDNSIP) {
			continue
		}
		out = append(out, server)
	}
	return out
}
