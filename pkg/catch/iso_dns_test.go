// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

type testISODNSPoolStore struct {
	pool netip.Prefix
	err  error
}

func (s *testISODNSPoolStore) ISOPool(context.Context) (netip.Prefix, error) {
	return s.pool, s.err
}

func TestISODNSFiltersAddressMatrixInEverySection(t *testing.T) {
	pool := netip.MustParsePrefix("172.30.0.0/16")
	addresses := []string{
		"1.1.1.1",         // public
		"10.0.0.1",        // private
		"127.0.0.1",       // loopback
		"169.254.1.1",     // link-local
		"100.100.100.100", // CGNAT / Quad100
		"172.30.128.2",    // current ISO pool
		"224.0.0.1",       // multicast
		"192.0.2.1",       // documentation
		"198.18.0.1",      // benchmarking/reserved
	}
	section := make([]dns.RR, 0, len(addresses)+1)
	for _, raw := range addresses {
		section = append(section, testISODNSA("example.com.", raw))
	}
	section = append(section, testISODNSAAAA("example.com.", "2606:4700:4700::1111"))

	msg := &dns.Msg{Answer: section, Ns: append([]dns.RR(nil), section...), Extra: append([]dns.RR(nil), section...)}
	filtered := filterISODNSMessage(msg, pool)
	for name, records := range map[string][]dns.RR{"answer": filtered.Answer, "authority": filtered.Ns, "additional": filtered.Extra} {
		if len(records) != 1 {
			t.Fatalf("%s records = %#v, want only public A", name, records)
		}
		got, ok := records[0].(*dns.A)
		if !ok || got.A.String() != "1.1.1.1" {
			t.Fatalf("%s retained = %#v, want 1.1.1.1 A", name, records[0])
		}
	}
}

func TestISODNSFiltersSVCBHTTPSHintsWithoutMutatingForwardedMessage(t *testing.T) {
	svcb := testISODNSRR(t, "svc.example. 60 IN SVCB 1 . ipv4hint=1.1.1.1,10.0.0.1 ipv6hint=2606:4700:4700::1111 alpn=h2")
	https := testISODNSRR(t, "web.example. 60 IN HTTPS 1 . ipv4hint=8.8.8.8,172.30.1.2 ipv6hint=2001:4860:4860::8888 alpn=h3")
	msg := &dns.Msg{Answer: []dns.RR{svcb}, Ns: []dns.RR{https}, Extra: []dns.RR{dns.Copy(svcb)}}
	before, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}

	filtered := filterISODNSMessage(msg, netip.MustParsePrefix("172.30.0.0/16"))
	for _, rr := range []dns.RR{filtered.Answer[0], filtered.Ns[0], filtered.Extra[0]} {
		raw := rr.String()
		if strings.Contains(raw, "ipv6hint") || strings.Contains(raw, "10.0.0.1") || strings.Contains(raw, "172.30.1.2") {
			t.Fatalf("filtered hints contain forbidden address: %s", raw)
		}
		if !strings.Contains(raw, "1.1.1.1") && !strings.Contains(raw, "8.8.8.8") {
			t.Fatalf("filtered hints lost public IPv4: %s", raw)
		}
		if rr == svcb || rr == https {
			t.Fatal("filter returned a forwarder-owned RR pointer")
		}
	}
	after, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("filter mutated forwarder-owned message\nbefore=%x\nafter=%x", before, after)
	}
}

func TestISODNSSVCBMandatoryTracksRemovedHintKeys(t *testing.T) {
	rr := testISODNSRR(t, "example.com. 60 IN HTTPS 1 . mandatory=ipv4hint,ipv6hint,alpn alpn=h2 ipv4hint=10.0.0.1 ipv6hint=2001:db8::1")
	original := rr.String()
	filtered := filterISODNSRecords([]dns.RR{rr}, netip.MustParsePrefix("172.30.0.0/16"))
	if len(filtered) != 1 {
		t.Fatalf("filtered = %#v, want retained HTTPS record", filtered)
	}
	raw := filtered[0].String()
	if !strings.Contains(raw, `mandatory="alpn"`) || strings.Contains(raw, "ipv4hint") || strings.Contains(raw, "ipv6hint") {
		t.Fatalf("mandatory keys not synchronized with removed hints: %s", raw)
	}
	msg := &dns.Msg{Answer: filtered}
	if _, err := msg.Pack(); err != nil {
		t.Fatalf("filtered mandatory record is not packable: %v", err)
	}
	if rr.String() != original {
		t.Fatalf("original mandatory record mutated: got %s want %s", rr, original)
	}
}

func TestISODNSRetainsNonAddressAndDNSSECRecords(t *testing.T) {
	records := []dns.RR{
		testISODNSRR(t, "alias.example. 60 IN CNAME target.example."),
		testISODNSRR(t, "example. 60 IN TXT \"safe\""),
		testISODNSRR(t, "example. 60 IN MX 10 mail.example."),
		testISODNSRR(t, "example. 60 IN NS ns.example."),
		testISODNSRR(t, "example. 60 IN SOA ns.example. hostmaster.example. 1 2 3 4 5"),
		testISODNSRR(t, "example. 60 IN DS 12345 8 2 49FD46E6C4B45C55D4AC"),
	}
	filtered := filterISODNSRecords(records, netip.MustParsePrefix("172.30.0.0/16"))
	if len(filtered) != len(records) {
		t.Fatalf("filtered records = %#v, want all non-address records", filtered)
	}
	for i := range records {
		if filtered[i].String() != records[i].String() || filtered[i] == records[i] {
			t.Fatalf("record %d was changed or not cloned: got=%p %s want=%p %s", i, filtered[i], filtered[i], records[i], records[i])
		}
	}
}

func TestISODNSRefusesLocalShortAndCurrentPoolReverseNames(t *testing.T) {
	store := &testISODNSPoolStore{pool: netip.MustParsePrefix("172.30.0.0/16")}
	forwarded := 0
	h := newISODNSHandler(store, func(context.Context, *dns.Msg) (*dns.Msg, error) {
		forwarded++
		return nil, errors.New("must not forward")
	}).(*isoDNSHandler)
	for _, name := range []string{"api", "api.yeet.internal.", "2.128.30.172.in-addr.arpa.", "30.172.in-addr.arpa."} {
		resp := h.responseFor(testISODNSQuery(name), testISODNSRemote("172.30.128.2"))
		if resp.Rcode != dns.RcodeRefused {
			t.Fatalf("%s rcode = %s, want REFUSED", name, dns.RcodeToString[resp.Rcode])
		}
	}
	store.pool = netip.MustParsePrefix("172.31.0.0/16")
	resp := h.responseFor(testISODNSQuery("2.128.30.172.in-addr.arpa."), testISODNSRemote("172.31.128.2"))
	if resp.Rcode == dns.RcodeRefused {
		t.Fatal("old pool reverse name remained refused after dynamic pool update")
	}
	if forwarded != 1 {
		t.Fatalf("forward calls = %d, want one for old-pool reverse after update", forwarded)
	}
}

func TestISODNSYeetRefusalIsLabelBounded(t *testing.T) {
	h := newISODNSHandler(&testISODNSPoolStore{pool: netip.MustParsePrefix("172.30.0.0/16")}, func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		return resp, nil
	}).(*isoDNSHandler)
	for _, name := range []string{"yeet.internal.", "api.yeet.internal."} {
		if got := h.responseFor(testISODNSQuery(name), testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeRefused {
			t.Fatalf("%s rcode = %s, want REFUSED", name, dns.RcodeToString[got])
		}
	}
	for _, name := range []string{"notyeet.internal.", "api.notyeet.internal."} {
		if got := h.responseFor(testISODNSQuery(name), testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeSuccess {
			t.Fatalf("%s rcode = %s, want forwarded success", name, dns.RcodeToString[got])
		}
	}
}

func TestISODNSRefusesYeetServiceReverseZoneButNotUnrelatedReverse(t *testing.T) {
	forwarded := 0
	h := newISODNSHandler(&testISODNSPoolStore{pool: netip.MustParsePrefix("172.30.0.0/16")}, func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		forwarded++
		resp := new(dns.Msg)
		resp.SetReply(req)
		return resp, nil
	}).(*isoDNSHandler)
	for _, name := range []string{"100.168.192.in-addr.arpa.", "3.100.168.192.in-addr.arpa."} {
		if got := h.responseFor(testISODNSQuery(name), testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeRefused {
			t.Fatalf("%s rcode = %s, want REFUSED", name, dns.RcodeToString[got])
		}
	}
	if got := h.responseFor(testISODNSQuery("2.0.0.192.in-addr.arpa."), testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeSuccess {
		t.Fatalf("unrelated reverse rcode = %s, want forwarded success", dns.RcodeToString[got])
	}
	if forwarded != 1 {
		t.Fatalf("forwarded = %d, want one unrelated reverse query", forwarded)
	}
}

func TestISODNSMalformedMultiQuestionAndFailures(t *testing.T) {
	pool := netip.MustParsePrefix("172.30.0.0/16")
	store := &testISODNSPoolStore{pool: pool}
	h := newISODNSHandler(store, func(context.Context, *dns.Msg) (*dns.Msg, error) {
		return nil, errors.New("forward failed")
	}).(*isoDNSHandler)
	for _, req := range []*dns.Msg{nil, {}, {Question: []dns.Question{{Name: "a.example.", Qtype: dns.TypeA}, {Name: "b.example.", Qtype: dns.TypeA}}}} {
		if got := h.responseFor(req, testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeFormatError {
			t.Fatalf("malformed rcode = %s, want FORMERR", dns.RcodeToString[got])
		}
	}
	if got := h.responseFor(testISODNSQuery("example.com."), testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeServerFailure {
		t.Fatalf("forward failure rcode = %s, want SERVFAIL", dns.RcodeToString[got])
	}
	store.err = errors.New("pool failed")
	if got := h.responseFor(testISODNSQuery("example.com."), testISODNSRemote("172.30.0.2")).Rcode; got != dns.RcodeServerFailure {
		t.Fatalf("pool failure rcode = %s, want SERVFAIL", dns.RcodeToString[got])
	}
}

func TestISODNSPreservesUpstreamRcodeAndClonesAllSections(t *testing.T) {
	upstream := new(dns.Msg)
	upstream.SetRcode(testISODNSQuery("missing.example."), dns.RcodeNameError)
	upstream.Ns = []dns.RR{testISODNSRR(t, "example. 60 IN SOA ns.example. hostmaster.example. 1 2 3 4 5")}
	upstream.Extra = []dns.RR{testISODNSA("ns.example.", "1.1.1.1")}
	h := newISODNSHandler(&testISODNSPoolStore{pool: netip.MustParsePrefix("172.30.0.0/16")}, func(context.Context, *dns.Msg) (*dns.Msg, error) {
		return upstream, nil
	}).(*isoDNSHandler)
	resp := h.responseFor(testISODNSQuery("missing.example."), testISODNSRemote("172.30.0.2"))
	if resp.Rcode != dns.RcodeNameError || len(resp.Ns) != 1 || len(resp.Extra) != 1 {
		t.Fatalf("response = %#v, want NXDOMAIN with authority/additional", resp)
	}
	if resp == upstream || resp.Ns[0] == upstream.Ns[0] || resp.Extra[0] == upstream.Extra[0] {
		t.Fatal("response aliases forwarder-owned message or records")
	}
}

func TestISODNSForwardingExcludesYeetSelfAndQuad100WithoutTailnetSpecialRouting(t *testing.T) {
	req := testISODNSQuery("host.example.ts.net.")
	cfg := &dns.ClientConfig{
		Servers: []string{yeetDNSHostIP, tailscaleDNSIP, "", "not-an-ip", "0.0.0.0", " [127.0.0.53] ", "192.168.1.53", "9.9.9.9"},
		Port:    "53",
		Search:  []string{"example.ts.net"},
	}
	wantServers := []string{"127.0.0.53", "192.168.1.53", "9.9.9.9"}
	if got := usableISOHostResolverServers(cfg.Servers); !slices.Equal(got, wantServers) {
		t.Fatalf("usableISOHostResolverServers = %#v, want %#v", got, wantServers)
	}
	var addresses []string
	resp, err := forwardISODNSViaResolverConfig(context.Background(), req, cfg, func(_ context.Context, got *dns.Msg, addr string) (*dns.Msg, error) {
		addresses = append(addresses, addr)
		if addr == "127.0.0.53:53" {
			return nil, errors.New("host stub unavailable")
		}
		if addr != "192.168.1.53:53" {
			t.Fatalf("unexpected upstream attempt %q", addr)
		}
		out := new(dns.Msg)
		out.SetReply(got)
		return out, nil
	})
	if err != nil || resp == nil {
		t.Fatalf("forwardISODNSViaResolverConfig = %#v, %v", resp, err)
	}
	if len(addresses) != 2 || addresses[0] != "127.0.0.53:53" || addresses[1] != "192.168.1.53:53" {
		t.Fatalf("upstreams = %#v, want host stub then RFC1918 resolver with no Yeet/Quad100/invalid candidates", addresses)
	}
}

func TestISODNSForwardingFailsClosedWithoutOrdinaryUpstream(t *testing.T) {
	cfg := &dns.ClientConfig{Servers: []string{yeetDNSHostIP, tailscaleDNSIP, "", "bad", "0.0.0.0"}, Port: "53"}
	called := false
	_, err := forwardISODNSViaResolverConfig(context.Background(), testISODNSQuery("example.com."), cfg, func(context.Context, *dns.Msg, string) (*dns.Msg, error) {
		called = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "no usable ordinary upstream DNS servers") {
		t.Fatalf("error = %v, want no ordinary upstream failure", err)
	}
	if called {
		t.Fatal("exchange called without an ordinary upstream")
	}
}

func TestISODNSForwardingRejectsISOListenerPort(t *testing.T) {
	cfg := &dns.ClientConfig{Servers: []string{"127.0.0.53", "192.168.1.53"}, Port: "5353"}
	called := false
	_, err := forwardISODNSViaResolverConfig(context.Background(), testISODNSQuery("example.com."), cfg, func(context.Context, *dns.Msg, string) (*dns.Msg, error) {
		called = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "ISO DNS listener port") {
		t.Fatalf("error = %v, want ISO DNS listener self-recursion failure", err)
	}
	if called {
		t.Fatal("exchange called for ISO DNS listener port")
	}
}

func TestISODNSUpstreamTruncationRetriesTCP(t *testing.T) {
	req := testISODNSQuery("large.example.")
	udpCalls, tcpCalls := 0, 0
	exchange := exchangeISODNSWithTCPFallback(
		func(_ context.Context, got *dns.Msg, addr string) (*dns.Msg, error) {
			udpCalls++
			if got.Question[0].Name != req.Question[0].Name || addr != "9.9.9.9:53" {
				t.Fatalf("UDP request = %#v addr=%q", got.Question, addr)
			}
			resp := new(dns.Msg)
			resp.SetReply(got)
			resp.Truncated = true
			return resp, nil
		},
		func(_ context.Context, got *dns.Msg, addr string) (*dns.Msg, error) {
			tcpCalls++
			resp := new(dns.Msg)
			resp.SetReply(got)
			resp.Answer = []dns.RR{testISODNSA(got.Question[0].Name, "1.1.1.1")}
			return resp, nil
		},
	)
	resp, err := exchange(context.Background(), req, "9.9.9.9:53")
	if err != nil || resp == nil || resp.Truncated || len(resp.Answer) != 1 {
		t.Fatalf("fallback response = %#v, %v", resp, err)
	}
	if udpCalls != 1 || tcpCalls != 1 {
		t.Fatalf("exchange calls udp=%d tcp=%d, want one each", udpCalls, tcpCalls)
	}
}

func TestISODNSAcceptsOnlyCurrentPoolSources(t *testing.T) {
	store := &testISODNSPoolStore{pool: netip.MustParsePrefix("172.30.0.0/16")}
	h := newISODNSHandler(store, func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Answer = []dns.RR{testISODNSA(req.Question[0].Name, "1.1.1.1")}
		return resp, nil
	})
	for _, source := range []string{"172.30.0.2", "172.30.128.2"} {
		writer := &testISODNSResponseWriter{remote: testISODNSRemote(source)}
		h.ServeDNS(writer, testISODNSQuery("example.com."))
		if writer.msg == nil || writer.msg.Rcode != dns.RcodeSuccess {
			t.Fatalf("source %s response = %#v, want success", source, writer.msg)
		}
	}
	tcpWriter := &testISODNSResponseWriter{remote: &net.TCPAddr{IP: net.ParseIP("172.30.128.3"), Port: 53000}}
	h.ServeDNS(tcpWriter, testISODNSQuery("example.com."))
	if tcpWriter.msg == nil || tcpWriter.msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("TCP source response = %#v, want success", tcpWriter.msg)
	}
	for _, source := range []string{"192.168.1.2", "1.1.1.1"} {
		writer := &testISODNSResponseWriter{remote: testISODNSRemote(source)}
		h.ServeDNS(writer, testISODNSQuery("example.com."))
		if writer.msg == nil || writer.msg.Rcode != dns.RcodeRefused {
			t.Fatalf("source %s response = %#v, want REFUSED", source, writer.msg)
		}
	}
	store.pool = netip.MustParsePrefix("172.31.0.0/16")
	writer := &testISODNSResponseWriter{remote: testISODNSRemote("172.30.0.2")}
	h.ServeDNS(writer, testISODNSQuery("example.com."))
	if writer.msg == nil || writer.msg.Rcode != dns.RcodeRefused {
		t.Fatalf("stale source response = %#v, want REFUSED after pool change", writer.msg)
	}
}

func TestISODNSFailsClosedForInvalidPoolAndMalformedRemoteAddress(t *testing.T) {
	h := newISODNSHandler(&testISODNSPoolStore{}, func(context.Context, *dns.Msg) (*dns.Msg, error) {
		t.Fatal("invalid pool must not forward")
		return nil, nil
	})
	writer := &testISODNSResponseWriter{remote: testISODNSRemote("172.30.0.2")}
	h.ServeDNS(writer, testISODNSQuery("example.com."))
	if writer.msg == nil || writer.msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("invalid pool response = %#v, want SERVFAIL", writer.msg)
	}

	h = newISODNSHandler(&testISODNSPoolStore{pool: netip.MustParsePrefix("172.30.0.0/16")}, func(context.Context, *dns.Msg) (*dns.Msg, error) {
		t.Fatal("malformed source must not forward")
		return nil, nil
	})
	for _, remote := range []net.Addr{nil, testStringAddr("not-an-address"), &net.UnixAddr{Name: "/tmp/dns", Net: "unix"}} {
		writer = &testISODNSResponseWriter{remote: remote}
		h.ServeDNS(writer, testISODNSQuery("example.com."))
		if writer.msg == nil || writer.msg.Rcode != dns.RcodeRefused {
			t.Fatalf("remote %#v response = %#v, want REFUSED", remote, writer.msg)
		}
	}
}

func FuzzFilterISODNSMessage(f *testing.F) {
	seed := &dns.Msg{
		Question: []dns.Question{{Name: "example.com.", Qtype: dns.TypeHTTPS, Qclass: dns.ClassINET}},
		Answer:   []dns.RR{testISODNSRR(f, "example.com. 60 IN HTTPS 1 . ipv4hint=1.1.1.1,10.0.0.1 ipv6hint=2001:db8::1")},
	}
	raw, err := seed.Pack()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		var msg dns.Msg
		if err := msg.Unpack(raw); err != nil {
			return
		}
		before, err := msg.Pack()
		if err != nil {
			return
		}
		filtered := filterISODNSMessage(&msg, netip.MustParsePrefix("172.30.0.0/16"))
		if _, err := filtered.Pack(); err != nil {
			t.Fatalf("filtered message is not packable: %v", err)
		}
		after, err := msg.Pack()
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("filter mutated input: err=%v", err)
		}
	})
}

func testISODNSQuery(name string) *dns.Msg {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return msg
}

func testISODNSA(name, raw string) *dns.A {
	return &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP(raw).To4()}
}

func testISODNSAAAA(name, raw string) *dns.AAAA {
	return &dns.AAAA{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP(raw)}
}

func testISODNSRR(tb testing.TB, raw string) dns.RR {
	tb.Helper()
	rr, err := dns.NewRR(raw)
	if err != nil {
		tb.Fatal(err)
	}
	return rr
}

func testISODNSRemote(raw string) net.Addr {
	return &net.UDPAddr{IP: net.ParseIP(raw), Port: 53000}
}

type testISODNSResponseWriter struct {
	remote net.Addr
	msg    *dns.Msg
}

func (w *testISODNSResponseWriter) LocalAddr() net.Addr  { return &net.UDPAddr{} }
func (w *testISODNSResponseWriter) RemoteAddr() net.Addr { return w.remote }
func (w *testISODNSResponseWriter) WriteMsg(msg *dns.Msg) error {
	w.msg = msg.Copy()
	return nil
}
func (w *testISODNSResponseWriter) Write(raw []byte) (int, error) { return len(raw), nil }
func (w *testISODNSResponseWriter) Close() error                  { return nil }
func (w *testISODNSResponseWriter) TsigStatus() error             { return nil }
func (w *testISODNSResponseWriter) TsigTimersOnly(bool)           {}
func (w *testISODNSResponseWriter) Hijack()                       {}

type testStringAddr string

func (a testStringAddr) Network() string { return "test" }
func (a testStringAddr) String() string  { return string(a) }
