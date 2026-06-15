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
	"regexp"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/yeetrun/yeet/pkg/db"
)

const (
	yeetDNSDomain       = "yeet.internal."
	yeetDNSHostIP       = "192.168.100.1"
	yeetDNSListenAddr   = yeetDNSHostIP + ":53"
	yeetDNSDefaultTTL   = uint32(30)
	yeetDNSReadTimeout  = 5 * time.Second
	yeetDNSWriteTimeout = 5 * time.Second
	tailscaleDNSIP      = "100.100.100.100"
)

var yeetDNSServiceLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type dnsDataStore interface {
	Get() (db.DataView, error)
}

func lookupYeetDNSName(dv db.DataView, qname string) (netip.Addr, bool) {
	name, ok := yeetDNSServiceNameFromQuery(qname)
	if !ok || !dv.Valid() {
		return netip.Addr{}, false
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		return netip.Addr{}, false
	}
	svcNet, ok := sv.SvcNetwork().GetOk()
	if !ok || !svcNet.IPv4.IsValid() {
		return netip.Addr{}, false
	}
	if !validYeetDNSServiceLabel(sv.Name()) {
		return netip.Addr{}, false
	}
	return svcNet.IPv4, true
}

func yeetDNSServiceNameFromQuery(qname string) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(qname)), ".")
	if name == "" {
		return "", false
	}
	if strings.HasSuffix(name, strings.TrimSuffix(yeetDNSDomain, ".")) {
		name = strings.TrimSuffix(name, "."+strings.TrimSuffix(yeetDNSDomain, "."))
	}
	if !validYeetDNSServiceLabel(name) {
		return "", false
	}
	return name, true
}

func validYeetDNSServiceLabel(name string) bool {
	return yeetDNSServiceLabelPattern.MatchString(name)
}

type dnsForwardFunc func(context.Context, *dns.Msg) (*dns.Msg, error)
type dnsExchangeFunc func(context.Context, *dns.Msg, string) (*dns.Msg, error)

type yeetDNSHandler struct {
	store   dnsDataStore
	forward dnsForwardFunc
}

func newYeetDNSHandler(store dnsDataStore, forward dnsForwardFunc) dns.Handler {
	if forward == nil {
		forward = forwardDNSViaHostResolver
	}
	return &yeetDNSHandler{store: store, forward: forward}
}

func (h *yeetDNSHandler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := h.responseFor(req)
	if err := w.WriteMsg(resp); err != nil {
		log.Printf("failed to write DNS response: %v", err)
	}
}

func (h *yeetDNSHandler) responseFor(req *dns.Msg) *dns.Msg {
	if len(req.Question) != 1 {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeFormatError)
		return resp
	}
	q := req.Question[0]
	if isYeetInternalDNSName(q.Name) || isShortYeetDNSCandidate(q.Name) {
		return h.responseForYeetName(req, q)
	}
	resp, err := h.forward(context.Background(), req)
	if err != nil {
		out := new(dns.Msg)
		out.SetRcode(req, dns.RcodeServerFailure)
		return out
	}
	if resp == nil {
		out := new(dns.Msg)
		out.SetRcode(req, dns.RcodeServerFailure)
		return out
	}
	return resp
}

func (h *yeetDNSHandler) responseForYeetName(req *dns.Msg, q dns.Question) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	dv, err := h.store.Get()
	if err != nil {
		resp.SetRcode(req, dns.RcodeServerFailure)
		return resp
	}
	addr, ok := lookupYeetDNSName(dv, q.Name)
	if !ok {
		resp.SetRcode(req, dns.RcodeNameError)
		return resp
	}
	if q.Qtype != dns.TypeA {
		return resp
	}
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: yeetDNSDefaultTTL},
		A:   net.IP(addr.AsSlice()).To4(),
	})
	return resp
}

func isYeetInternalDNSName(qname string) bool {
	return strings.HasSuffix(strings.ToLower(dns.Fqdn(qname)), yeetDNSDomain)
}

func isShortYeetDNSCandidate(qname string) bool {
	name := strings.TrimSuffix(strings.TrimSpace(qname), ".")
	return name != "" && !strings.Contains(name, ".")
}

func forwardDNSViaHostResolver(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, fmt.Errorf("read host resolv.conf: %w", err)
	}
	client := &dns.Client{
		Net:          "udp",
		ReadTimeout:  yeetDNSReadTimeout,
		WriteTimeout: yeetDNSWriteTimeout,
	}
	return forwardDNSViaResolverConfig(ctx, req, cfg, exchangeDNSWithClient(client))
}

func exchangeDNSWithClient(client *dns.Client) dnsExchangeFunc {
	return func(ctx context.Context, req *dns.Msg, addr string) (*dns.Msg, error) {
		resp, _, err := client.ExchangeContext(ctx, req, addr)
		return resp, err
	}
}

func forwardDNSViaResolverConfig(ctx context.Context, req *dns.Msg, cfg *dns.ClientConfig, exchange dnsExchangeFunc) (*dns.Msg, error) {
	if cfg == nil {
		return nil, fmt.Errorf("host resolver config is nil")
	}
	servers := cfg.Servers
	port := cfg.Port
	if port == "" {
		port = "53"
	}
	if shouldForwardViaTailscaleDNS(req, cfg.Search) {
		servers = []string{tailscaleDNSIP}
		port = "53"
	}
	return forwardDNSViaServers(ctx, req, servers, port, exchange)
}

func shouldForwardViaTailscaleDNS(req *dns.Msg, searchDomains []string) bool {
	if len(req.Question) != 1 {
		return false
	}
	return isTailnetDNSName(req.Question[0].Name, searchDomains)
}

func isTailnetDNSName(qname string, searchDomains []string) bool {
	name := normalizeDNSDomain(qname)
	if name == "ts.net" || strings.HasSuffix(name, ".ts.net") {
		return true
	}
	for _, domain := range searchDomains {
		domain = normalizeDNSDomain(domain)
		if domain == "" || (domain != "ts.net" && !strings.HasSuffix(domain, ".ts.net")) {
			continue
		}
		if name == domain || strings.HasSuffix(name, "."+domain) {
			return true
		}
	}
	return false
}

func normalizeDNSDomain(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(dns.Fqdn(name))), ".")
}

func forwardDNSViaServers(ctx context.Context, req *dns.Msg, servers []string, port string, exchange dnsExchangeFunc) (*dns.Msg, error) {
	var lastErr error
	for _, server := range servers {
		resp, err := exchange(ctx, req, net.JoinHostPort(server, port))
		if err == nil && resp != nil {
			return resp, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("host resolver has no upstream DNS servers")
}
