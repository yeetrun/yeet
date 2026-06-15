// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/miekg/dns"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestLookupYeetDNSNameResolvesSvcNetworkShortAndFQDN(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{
		"foo": {
			Name:        "foo",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
		},
		"bar": {
			Name:        "bar",
			ServiceType: db.ServiceTypeVM,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.4")},
		},
		"lan-only": {
			Name:        "lan-only",
			ServiceType: db.ServiceTypeVM,
		},
	}}

	tests := []struct {
		name string
		want string
	}{
		{name: "foo", want: "192.168.100.3"},
		{name: "foo.", want: "192.168.100.3"},
		{name: "foo.yeet.internal", want: "192.168.100.3"},
		{name: "foo.yeet.internal.", want: "192.168.100.3"},
		{name: "bar.yeet.internal.", want: "192.168.100.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lookupYeetDNSName(data.View(), tt.name)
			if !ok {
				t.Fatalf("lookupYeetDNSName(%q) ok=false, want true", tt.name)
			}
			if got.String() != tt.want {
				t.Fatalf("lookupYeetDNSName(%q) = %s, want %s", tt.name, got, tt.want)
			}
		})
	}

	for _, name := range []string{"missing", "missing.yeet.internal.", "lan-only.yeet.internal.", "foo.example.com."} {
		if got, ok := lookupYeetDNSName(data.View(), name); ok {
			t.Fatalf("lookupYeetDNSName(%q) = %s, true; want false", name, got)
		}
	}
}

func TestLookupYeetDNSNameSkipsUnsafeDNSLabels(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{
		"bad_name": {
			Name:        "bad_name",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.5")},
		},
		"ends-": {
			Name:        "ends-",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.6")},
		},
	}}
	if got, ok := lookupYeetDNSName(data.View(), "bad_name.yeet.internal."); ok {
		t.Fatalf("bad_name resolved to %s, want unsafe names skipped", got)
	}
	if got, ok := lookupYeetDNSName(data.View(), "ends-.yeet.internal."); ok {
		t.Fatalf("ends- resolved to %s, want unsafe names skipped", got)
	}
}

func TestYeetDNSHandlerAnswersInternalARecords(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{
		"foo": {
			Name:        "foo",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
		},
	}}
	handler := newYeetDNSHandler(fakeDNSStore{view: data.View()}, nil)

	resp := exchangeYeetDNSForTest(t, handler, newAQuestion("foo.yeet.internal."))
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("Rcode = %s, want success", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answers = %#v, want one A record", resp.Answer)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("answer type = %T, want *dns.A", resp.Answer[0])
	}
	if a.A.String() != "192.168.100.3" {
		t.Fatalf("A = %s, want 192.168.100.3", a.A)
	}
	if a.Hdr.Name != "foo.yeet.internal." || a.Hdr.Ttl != yeetDNSDefaultTTL {
		t.Fatalf("answer header = %#v", a.Hdr)
	}
}

func TestYeetDNSHandlerRefreshesStoreForNewServices(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db.json")
	servicesRoot := filepath.Join(root, "services")
	if err := db.NewStore(dbPath, servicesRoot).Set(&db.Data{
		DataVersion: db.CurrentDataVersion,
		Services:    map[string]*db.Service{},
	}); err != nil {
		t.Fatalf("seed DB: %v", err)
	}
	handler := newYeetDNSHandler(freshDNSStore{dbPath: dbPath, servicesRoot: servicesRoot}, nil)

	resp := exchangeYeetDNSForTest(t, handler, newAQuestion("later.yeet.internal."))
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("initial Rcode = %s, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
	}

	if err := db.NewStore(dbPath, servicesRoot).Set(&db.Data{
		DataVersion: db.CurrentDataVersion,
		Services: map[string]*db.Service{
			"later": {
				Name:        "later",
				ServiceType: db.ServiceTypeSystemd,
				SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.9")},
			},
		},
	}); err != nil {
		t.Fatalf("update DB: %v", err)
	}

	resp = exchangeYeetDNSForTest(t, handler, newAQuestion("later.yeet.internal."))
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("updated Rcode = %s, want success", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answers = %#v, want one A record", resp.Answer)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("answer type = %T, want *dns.A", resp.Answer[0])
	}
	if a.A.String() != "192.168.100.9" {
		t.Fatalf("A = %s, want 192.168.100.9", a.A)
	}
}

func TestYeetDNSHandlerReturnsNXDomainForUnknownInternalName(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{}}
	forwarded := false
	handler := newYeetDNSHandler(fakeDNSStore{view: data.View()}, func(context.Context, *dns.Msg) (*dns.Msg, error) {
		forwarded = true
		return nil, nil
	})

	resp := exchangeYeetDNSForTest(t, handler, newAQuestion("missing.yeet.internal."))
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("Rcode = %s, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
	}
	if forwarded {
		t.Fatal("unknown yeet.internal query was forwarded")
	}
}

func TestYeetDNSHandlerReturnsNoDataForExistingInternalAAAA(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{
		"foo": {
			Name:        "foo",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
		},
	}}
	handler := newYeetDNSHandler(fakeDNSStore{view: data.View()}, nil)
	req := new(dns.Msg)
	req.SetQuestion("foo.yeet.internal.", dns.TypeAAAA)

	resp := exchangeYeetDNSForTest(t, handler, req)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("Rcode = %s, want success no-data", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 0 {
		t.Fatalf("answers = %#v, want no AAAA answers", resp.Answer)
	}
}

func TestYeetDNSHandlerForwardsExternalQueries(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{}}
	var forwardedName string
	handler := newYeetDNSHandler(fakeDNSStore{view: data.View()}, func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		forwardedName = req.Question[0].Name
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 10},
			A:   net.ParseIP("93.184.216.34").To4(),
		})
		return resp, nil
	})

	resp := exchangeYeetDNSForTest(t, handler, newAQuestion("example.com."))
	if forwardedName != "example.com." {
		t.Fatalf("forwarded name = %q, want example.com.", forwardedName)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("response = rcode %s answers %#v", dns.RcodeToString[resp.Rcode], resp.Answer)
	}
}

func TestForwardDNSViaServersReturnsFirstResponse(t *testing.T) {
	req := newAQuestion("example.com.")
	response := new(dns.Msg)
	response.SetReply(req)
	var addrs []string
	got, err := forwardDNSViaServers(context.Background(), req, []string{"192.0.2.1", "2001:db8::1"}, "53", func(_ context.Context, req *dns.Msg, addr string) (*dns.Msg, error) {
		addrs = append(addrs, addr)
		if len(addrs) == 1 {
			return nil, errors.New("first resolver failed")
		}
		return response, nil
	})

	if err != nil {
		t.Fatalf("forwardDNSViaServers returned error: %v", err)
	}
	if got != response {
		t.Fatalf("response = %p, want %p", got, response)
	}
	wantAddrs := []string{"192.0.2.1:53", "[2001:db8::1]:53"}
	if len(addrs) != len(wantAddrs) {
		t.Fatalf("addrs = %#v, want %#v", addrs, wantAddrs)
	}
	for i := range wantAddrs {
		if addrs[i] != wantAddrs[i] {
			t.Fatalf("addrs = %#v, want %#v", addrs, wantAddrs)
		}
	}
}

func TestForwardDNSViaResolverConfigRoutesTailnetQueriesToTailscaleDNS(t *testing.T) {
	req := newAQuestion("plex.shayne.ts.net.")
	response := new(dns.Msg)
	response.SetReply(req)
	var addrs []string

	got, err := forwardDNSViaResolverConfig(context.Background(), req, &dns.ClientConfig{
		Servers: []string{"192.0.2.53"},
		Search:  []string{"shayne.ts.net"},
		Port:    "53",
	}, func(_ context.Context, _ *dns.Msg, addr string) (*dns.Msg, error) {
		addrs = append(addrs, addr)
		return response, nil
	})

	if err != nil {
		t.Fatalf("forwardDNSViaResolverConfig returned error: %v", err)
	}
	if got != response {
		t.Fatalf("response = %p, want %p", got, response)
	}
	want := []string{"100.100.100.100:53"}
	if len(addrs) != len(want) || addrs[0] != want[0] {
		t.Fatalf("addrs = %#v, want %#v", addrs, want)
	}
}

func TestForwardDNSViaResolverConfigKeepsPublicQueriesOnHostResolver(t *testing.T) {
	req := newAQuestion("example.com.")
	response := new(dns.Msg)
	response.SetReply(req)
	var addrs []string

	_, err := forwardDNSViaResolverConfig(context.Background(), req, &dns.ClientConfig{
		Servers: []string{"192.0.2.53"},
		Search:  []string{"shayne.ts.net"},
		Port:    "53",
	}, func(_ context.Context, _ *dns.Msg, addr string) (*dns.Msg, error) {
		addrs = append(addrs, addr)
		return response, nil
	})

	if err != nil {
		t.Fatalf("forwardDNSViaResolverConfig returned error: %v", err)
	}
	want := []string{"192.0.2.53:53"}
	if len(addrs) != len(want) || addrs[0] != want[0] {
		t.Fatalf("addrs = %#v, want %#v", addrs, want)
	}
}

func TestForwardDNSViaServersReturnsLastResolverError(t *testing.T) {
	req := newAQuestion("example.com.")
	lastErr := errors.New("last resolver failed")
	_, err := forwardDNSViaServers(context.Background(), req, []string{"192.0.2.1", "192.0.2.2"}, "53", func(_ context.Context, _ *dns.Msg, addr string) (*dns.Msg, error) {
		if addr == "192.0.2.2:53" {
			return nil, lastErr
		}
		return nil, errors.New("first resolver failed")
	})

	if !errors.Is(err, lastErr) {
		t.Fatalf("error = %v, want %v", err, lastErr)
	}
}

func TestForwardDNSViaServersRejectsEmptyResolverList(t *testing.T) {
	_, err := forwardDNSViaServers(context.Background(), newAQuestion("example.com."), nil, "53", nil)
	if err == nil {
		t.Fatal("forwardDNSViaServers returned nil error")
	}
}

type fakeDNSStore struct {
	view db.DataView
	err  error
}

func (s fakeDNSStore) Get() (db.DataView, error) {
	return s.view, s.err
}

func newAQuestion(name string) *dns.Msg {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return msg
}

func exchangeYeetDNSForTest(t *testing.T, handler dns.Handler, req *dns.Msg) *dns.Msg {
	t.Helper()
	rec := &dnsResponseRecorder{}
	handler.ServeDNS(rec, req)
	if rec.msg == nil {
		t.Fatal("handler did not write a DNS response")
	}
	return rec.msg
}

type dnsResponseRecorder struct {
	msg *dns.Msg
}

func (r *dnsResponseRecorder) LocalAddr() net.Addr         { return dummyDNSAddr("local") }
func (r *dnsResponseRecorder) RemoteAddr() net.Addr        { return dummyDNSAddr("remote") }
func (r *dnsResponseRecorder) WriteMsg(msg *dns.Msg) error { r.msg = msg; return nil }
func (r *dnsResponseRecorder) Write([]byte) (int, error)   { return 0, nil }
func (r *dnsResponseRecorder) Close() error                { return nil }
func (r *dnsResponseRecorder) TsigStatus() error           { return nil }
func (r *dnsResponseRecorder) TsigTimersOnly(bool)         {}
func (r *dnsResponseRecorder) Hijack()                     {}

type dummyDNSAddr string

func (a dummyDNSAddr) Network() string { return "udp" }
func (a dummyDNSAddr) String() string  { return string(a) }

var _ = context.Background
