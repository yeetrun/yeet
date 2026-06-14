# Catch-Owned DNS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a catch-owned DNS server that gives `svc` workloads and yeet-managed VMs short-name and `*.yeet.internal` service discovery while forwarding non-yeet DNS through the catch host resolver.

**Architecture:** `catch` runs a sibling `yeet-dns.service` bound to `192.168.100.1:53` on the host-side yeet private address. The DNS handler uses `github.com/miekg/dns`, answers A records from `Service.SvcNetwork`, returns NXDOMAIN for unknown `*.yeet.internal`, and forwards all other names to upstream resolvers from the host resolver config. Resolver metadata for service netns and VM guest network configs points private networking at `192.168.100.1` and adds `yeet.internal` as the search/routing domain; explicit `DEFAULT_NS` remains the surgical opt-out.

**Tech Stack:** Go, `github.com/miekg/dns`, existing `pkg/db`, existing `pkg/svc` systemd unit generation, existing service netns and VM metadata renderers.

---

## Context And Constraints

- Use GitButler for version-control writes. Do not run raw `git add`, `git commit`, `git push`, `git checkout`, `git merge`, or `git rebase`.
- Work on the existing GitButler branch `codex/catch-dns-service-discovery`.
- The workspace is busy with unrelated snapshot/VM recovery branches. Do not modify `pkg/catch/recovery_*`, `pkg/catch/vm_snapshot*`, or the existing unassigned full-state-restore plan unless a later conflict forces a review.
- `root@pve1` is the machine SSH host for catch alias `yeet-pve1`; `root@hetz` is the machine SSH host for catch alias `yeet-hetz`.
- The user has authorized stopping, starting, or changing services on those hosts if something occupies `192.168.100.1:53`.
- v1 does not add nftables or iptables DNS capture.
- v1 does not add DB migrations. Existing running workloads pick up resolver metadata after restart, redeploy, or VM reconfiguration.

## File Structure

- Create `pkg/catch/dns.go`: DNS constants, yeet-local DB lookup, miekg/dns handler, host resolver forwarding, and foreground server entrypoint.
- Create `pkg/catch/dns_test.go`: pure lookup tests and DNS handler tests using miekg/dns messages and fake forwarders.
- Create `pkg/catch/dns_service.go`: install and start `yeet-dns.service` as a catch-managed systemd unit.
- Create `pkg/catch/dns_service_test.go`: systemd unit generation and install/start behavior tests.
- Modify `pkg/catch/catch.go`: install `yeet-dns.service` during `Server.Start()` after `yeet-ns.service`.
- Modify `cmd/catch/catch.go`: add a local foreground `dns` command that starts the DNS server after data dirs/config are prepared and before runtime/containerd validation.
- Modify `cmd/catch/catch_test.go`: cover `dns` local command routing without runtime validation.
- Modify `pkg/catch/installer_file.go`: default service netns `resolv.conf` to yeet DNS and `yeet.internal`, while preserving explicit `DEFAULT_NS`.
- Modify `pkg/catch/installer_file_test.go`: cover default and opt-out resolver rendering.
- Modify `pkg/catch/vm_metadata.go`: render yeet DNS nameserver, `yeet.internal` search/routing domain, and `DNSDefaultRoute=false` for svc links when LAN is also present.
- Modify `pkg/catch/vm_metadata_test.go`: cover legacy netplan, fast Ubuntu networkd, NixOS networkd metadata, `DEFAULT_NS` opt-out, and `svc,lan` DNS routing.
- Modify `pkg/catch/vm_network.go`: mark svc metadata as route-only DNS when a VM also has LAN networking.
- Modify `pkg/catch/vm_network_test.go`: cover metadata networks for `svc,lan`.
- Modify `go.mod` and `go.sum` through normal Go tooling so `github.com/miekg/dns` becomes a direct dependency if needed.
- Modify `website/docs/concepts/networking.mdx`: document `yeet.internal`, short-name resolution, `DEFAULT_NS` opt-out, and the `svc,lan` behavior.
- Modify `website/docs/payloads/vms.mdx`: mention that `svc` and `svc,lan` VMs get yeet-local DNS; LAN-only VMs do not.

---

### Task 1: Add Pure Yeet DNS Lookup Rules

**Files:**
- Create: `pkg/catch/dns.go`
- Create: `pkg/catch/dns_test.go`

- [ ] **Step 1: Write the failing lookup tests**

Add this file:

```go
// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestLookupYeetDNSNameResolvesSvcNetworkShortAndFQDN(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{
		"foo": {
			Name:        "foo",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
		},
		"bar": {
			Name:        "bar",
			ServiceType: db.ServiceTypeVM,
			SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.4")},
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
			SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.5")},
		},
		"ends-": {
			Name:        "ends-",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.6")},
		},
	}}
	if got, ok := lookupYeetDNSName(data.View(), "bad_name.yeet.internal."); ok {
		t.Fatalf("bad_name resolved to %s, want unsafe names skipped", got)
	}
	if got, ok := lookupYeetDNSName(data.View(), "ends-.yeet.internal."); ok {
		t.Fatalf("ends- resolved to %s, want unsafe names skipped", got)
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

func (r *dnsResponseRecorder) LocalAddr() net.Addr                { return dummyDNSAddr("local") }
func (r *dnsResponseRecorder) RemoteAddr() net.Addr               { return dummyDNSAddr("remote") }
func (r *dnsResponseRecorder) WriteMsg(msg *dns.Msg) error        { r.msg = msg; return nil }
func (r *dnsResponseRecorder) Write([]byte) (int, error)          { return 0, nil }
func (r *dnsResponseRecorder) Close() error                       { return nil }
func (r *dnsResponseRecorder) TsigStatus() error                  { return nil }
func (r *dnsResponseRecorder) TsigTimersOnly(bool)                {}
func (r *dnsResponseRecorder) Hijack()                            {}

type dummyDNSAddr string

func (a dummyDNSAddr) Network() string { return "udp" }
func (a dummyDNSAddr) String() string  { return string(a) }

var _ = context.Background
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestLookupYeetDNSName' -count=1
```

Expected: FAIL because `lookupYeetDNSName` is undefined.

- [ ] **Step 3: Implement the minimal lookup code**

Create `pkg/catch/dns.go` with this initial content:

```go
// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"net/netip"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	yeetDNSDomain       = "yeet.internal."
	yeetDNSHostIP       = "192.168.100.1"
	yeetDNSListenAddr   = yeetDNSHostIP + ":53"
	yeetDNSDefaultTTL   = uint32(30)
	yeetDNSReadTimeout  = 5 * time.Second
	yeetDNSWriteTimeout = 5 * time.Second
)

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
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./pkg/catch -run 'TestLookupYeetDNSName' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run gofmt**

Run:

```bash
gofmt -w pkg/catch/dns.go pkg/catch/dns_test.go
```

Expected: no output.

---

### Task 2: Add miekg/dns Handler And Forwarding

**Files:**
- Modify: `pkg/catch/dns.go`
- Modify: `pkg/catch/dns_test.go`
- Modify through Go tooling: `go.mod`, `go.sum`

- [ ] **Step 1: Write failing DNS handler tests**

Append these tests to `pkg/catch/dns_test.go`:

```go
func TestYeetDNSHandlerAnswersInternalARecords(t *testing.T) {
	data := &db.Data{Services: map[string]*db.Service{
		"foo": {
			Name:        "foo",
			ServiceType: db.ServiceTypeSystemd,
			SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
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
			SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestYeetDNSHandler' -count=1
```

Expected: FAIL because `newYeetDNSHandler` is undefined.

- [ ] **Step 3: Implement handler, forwarder, and foreground server**

Update the `pkg/catch/dns.go` import block to include these additional packages:

```go
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/miekg/dns"
```

Then extend `pkg/catch/dns.go` with this code:

```go
type dnsForwardFunc func(context.Context, *dns.Msg) (*dns.Msg, error)

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
	var lastErr error
	for _, server := range cfg.Servers {
		addr := net.JoinHostPort(server, cfg.Port)
		resp, _, err := client.ExchangeContext(ctx, req, addr)
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

func RunDNSServer(ctx context.Context, cfg *Config) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("DNS server requires a DB")
	}
	server := &dns.Server{
		Addr:         yeetDNSListenAddr,
		Net:          "udp",
		Handler:      newYeetDNSHandler(cfg.DB, nil),
		ReadTimeout:  yeetDNSReadTimeout,
		WriteTimeout: yeetDNSWriteTimeout,
	}
	errc := make(chan error, 1)
	go func() {
		log.Printf("starting yeet DNS on udp/%s", yeetDNSListenAddr)
		errc <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return server.ShutdownContext(shutdownCtx)
	case err := <-errc:
		return err
	}
}
```

- [ ] **Step 4: Run gofmt and targeted tests**

Run:

```bash
gofmt -w pkg/catch/dns.go pkg/catch/dns_test.go
go test ./pkg/catch -run 'TestLookupYeetDNSName|TestYeetDNSHandler' -count=1
```

Expected: PASS.

- [ ] **Step 5: Update module metadata**

Run:

```bash
go mod tidy
```

Expected: `go.mod` lists `github.com/miekg/dns` as a direct dependency if the import requires it; `go.sum` remains valid.

---

### Task 3: Install yeet-dns.service From catch Startup

**Files:**
- Create: `pkg/catch/dns_service.go`
- Create: `pkg/catch/dns_service_test.go`
- Modify: `pkg/catch/catch.go`

- [ ] **Step 1: Write failing service install tests**

Create `pkg/catch/dns_service_test.go`:

```go
// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestNewYeetDNSUnitRunsCatchDNSCommand(t *testing.T) {
	unit := newYeetDNSUnit("/usr/local/bin/catch", "/srv/yeet")
	files, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	raw, err := os.ReadFile(files[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service\n",
		"ExecStart=/usr/local/bin/catch -data-dir /srv/yeet dns\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestInstallYeetDNSServiceInstallsAndStarts(t *testing.T) {
	root := chdirTempForDNSService(t)
	var calls []string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: filepath.Join(root, "systemd", "yeet-dns.service"),
		unitActive:  func(string) bool { return false },
		systemctl: func(args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}
	wantCalls := []string{"daemon-reload", "enable yeet-dns.service", "start yeet-dns.service"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", calls, wantCalls)
	}
	installed, err := os.ReadFile(filepath.Join(root, "systemd", "yeet-dns.service"))
	if err != nil {
		t.Fatalf("read installed unit: %v", err)
	}
	if !strings.Contains(string(installed), "ExecStart=/usr/local/bin/catch -data-dir /srv/yeet dns\n") {
		t.Fatalf("installed unit missing ExecStart:\n%s", string(installed))
	}
}

func TestInstallYeetDNSServicePreservesActiveService(t *testing.T) {
	root := chdirTempForDNSService(t)
	var calls []string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: filepath.Join(root, "systemd", "yeet-dns.service"),
		unitActive:  func(string) bool { return true },
		systemctl: func(args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}
	wantCalls := []string{"daemon-reload", "enable yeet-dns.service"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestInstallYeetDNSServicePropagatesStartErrors(t *testing.T) {
	root := chdirTempForDNSService(t)
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: filepath.Join(root, "systemd", "yeet-dns.service"),
		unitActive:  func(string) bool { return false },
		systemctl: func(args ...string) error {
			if strings.Join(args, " ") == "start yeet-dns.service" {
				return errors.New("bind failed")
			}
			return nil
		},
	})
	err := installYeetDNSService("/srv/yeet")
	if err == nil || !strings.Contains(err.Error(), "failed to start yeet-dns service") {
		t.Fatalf("installYeetDNSService error = %v, want start wrapper", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'Test.*YeetDNSService|TestNewYeetDNSUnit' -count=1
```

Expected: FAIL because DNS service installer symbols are undefined.

- [ ] **Step 3: Implement service installer**

Create `pkg/catch/dns_service.go`:

```go
// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
)

var (
	catchExecutablePath = os.Executable
	catchSystemdUnitPath = func(unit string) string {
		return filepath.Join("/etc/systemd/system", unit)
	}
	catchSystemdUnitActive = systemdUnitIsActive
	catchSystemctl = runCatchSystemctl
)

func systemdUnitIsActive(unit string) bool {
	return catchSystemctl("is-active", "--quiet", unit) == nil
}

func runCatchSystemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return nil
}

func newYeetDNSUnit(catchBin, dataDir string) *svc.SystemdUnit {
	return &svc.SystemdUnit{
		Name:       "yeet-dns",
		Executable: catchBin,
		Arguments:  []string{"-data-dir", dataDir, "dns"},
		Requires:   "yeet-ns.service",
		After:      "yeet-ns.service",
		WantedBy:   "multi-user.target",
	}
}

func installYeetDNSService(dataDir string) error {
	catchBin, err := catchExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to resolve catch binary path: %w", err)
	}
	unitFiles, err := newYeetDNSUnit(catchBin, dataDir).WriteOutUnitFiles(".")
	if err != nil {
		return fmt.Errorf("failed to write yeet-dns unit: %w", err)
	}
	defer removeGeneratedYeetDNSFiles(unitFiles)
	changed, err := yeetDNSUnitChanged(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		return err
	}
	alreadyActive := catchSystemdUnitActive("yeet-dns.service")
	if !changed && alreadyActive {
		return nil
	}
	return installGeneratedYeetDNSService(unitFiles[db.ArtifactSystemdUnit], changed, alreadyActive)
}

func yeetDNSUnitChanged(generatedUnit string) (bool, error) {
	same, err := fileutil.Identical(catchSystemdUnitPath("yeet-dns.service"), generatedUnit)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to compare yeet-dns unit: %w", err)
	}
	return !same, nil
}

func installGeneratedYeetDNSService(generatedUnit string, changed bool, alreadyActive bool) error {
	dst := catchSystemdUnitPath("yeet-dns.service")
	if changed {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("failed to create yeet-dns systemd dir: %w", err)
		}
		if err := fileutil.CopyFile(generatedUnit, dst); err != nil {
			return fmt.Errorf("failed to install yeet-dns unit: %w", err)
		}
		if err := catchSystemctl("daemon-reload"); err != nil {
			return fmt.Errorf("failed to reload systemd for yeet-dns: %w", err)
		}
	}
	if err := catchSystemctl("enable", "yeet-dns.service"); err != nil {
		return fmt.Errorf("failed to enable yeet-dns service: %w", err)
	}
	if alreadyActive {
		log.Printf("installed updated yeet-dns.service; leaving active DNS server running")
		return nil
	}
	if err := catchSystemctl("start", "yeet-dns.service"); err != nil {
		return fmt.Errorf("failed to start yeet-dns service: %w", err)
	}
	return nil
}

func removeGeneratedYeetDNSFiles(files map[db.ArtifactName]string) {
	for _, path := range files {
		_ = os.Remove(path)
	}
}
```

- [ ] **Step 4: Add test fakes**

Append to `pkg/catch/dns_service_test.go`:

```go
type yeetDNSServiceFakes struct {
	catchBin      string
	executableErr error
	systemdPath   string
	unitActive    func(string) bool
	systemctl     func(...string) error
}

func withYeetDNSServiceFakes(t *testing.T, fakes yeetDNSServiceFakes) {
	t.Helper()
	oldExecutable := catchExecutablePath
	oldUnitPath := catchSystemdUnitPath
	oldUnitActive := catchSystemdUnitActive
	oldSystemctl := catchSystemctl
	catchExecutablePath = func() (string, error) {
		if fakes.executableErr != nil {
			return "", fakes.executableErr
		}
		return fakes.catchBin, nil
	}
	catchSystemdUnitPath = func(unit string) string {
		if unit != "yeet-dns.service" {
			t.Fatalf("catchSystemdUnitPath(%q), want yeet-dns.service", unit)
		}
		return fakes.systemdPath
	}
	catchSystemdUnitActive = fakes.unitActive
	catchSystemctl = fakes.systemctl
	t.Cleanup(func() {
		catchExecutablePath = oldExecutable
		catchSystemdUnitPath = oldUnitPath
		catchSystemdUnitActive = oldUnitActive
		catchSystemctl = oldSystemctl
	})
}

func chdirTempForDNSService(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})
	return root
}
```

- [ ] **Step 5: Wire startup**

Modify `pkg/catch/catch.go` in `Server.Start()` so DNS installs after the bridge namespace:

```go
	if err := installYeetNSService(); err != nil {
		log.Fatalf("Failed to install bridge service: %v", err)
	}
	if err := installYeetDNSService(s.cfg.RootDir); err != nil {
		log.Fatalf("Failed to install DNS service: %v", err)
	}
	if err := installDockerPrereqs(s); err != nil {
```

- [ ] **Step 6: Run gofmt and targeted tests**

Run:

```bash
gofmt -w pkg/catch/dns_service.go pkg/catch/dns_service_test.go pkg/catch/catch.go
go test ./pkg/catch -run 'Test.*YeetDNSService|TestNewYeetDNSUnit' -count=1
```

Expected: PASS.

---

### Task 4: Add catch dns Foreground Command

**Files:**
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

- [ ] **Step 1: Write failing command routing test**

Append to `cmd/catch/catch_test.go`:

```go
func TestHandleLocalCommandDNSRunsWithoutRuntimeValidation(t *testing.T) {
	oldRunDNS := runDNSFn
	oldValidateRuntime := validateCatchRuntimeFn
	t.Cleanup(func() {
		runDNSFn = oldRunDNS
		validateCatchRuntimeFn = oldValidateRuntime
	})

	var called bool
	runDNSFn = func(ctx context.Context, cfg *catch.Config) error {
		called = true
		if cfg.RootDir != "/srv/yeet" {
			t.Fatalf("cfg.RootDir = %q, want /srv/yeet", cfg.RootDir)
		}
		return nil
	}
	validateCatchRuntimeFn = func(string) error {
		t.Fatal("runtime validation should not run for catch dns")
		return nil
	}

	handled, err := handleLocalCommand([]string{"dns"}, &catch.Config{RootDir: "/srv/yeet"}, "/srv/yeet", io.Discard)
	if err != nil {
		t.Fatalf("handleLocalCommand dns returned error: %v", err)
	}
	if !handled || !called {
		t.Fatalf("handled=%v called=%v, want true/true", handled, called)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/catch -run TestHandleLocalCommandDNSRunsWithoutRuntimeValidation -count=1
```

Expected: FAIL because `runDNSFn` is undefined and `dns` is not handled.

- [ ] **Step 3: Wire the command**

In `cmd/catch/catch.go`, add this variable near the other function variables:

```go
	runDNSFn                               = catch.RunDNSServer
```

Then modify `handleLocalCommand`:

```go
	case "dns":
		return true, runDNSFn(context.Background(), scfg)
	case "install":
```

- [ ] **Step 4: Run gofmt and targeted tests**

Run:

```bash
gofmt -w cmd/catch/catch.go cmd/catch/catch_test.go
go test ./cmd/catch -run 'TestHandleLocalCommandDNS|TestRunCatchProcessHandlesLocalCommand|TestRunCatchProcessReturnsRuntimeValidationError' -count=1
```

Expected: PASS.

---

### Task 5: Change Service NetNS Resolver Defaults

**Files:**
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`

- [ ] **Step 1: Write failing resolver default tests**

Add these tests near `TestBuildNetNSResolvConfIncludesOptionalSearchDomains`:

```go
func TestDefaultNetNSResolvConfUsesYeetDNSByDefault(t *testing.T) {
	t.Setenv("DEFAULT_NS", "")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := defaultNetNSResolvConf()
	want := "nameserver 192.168.100.1\nsearch yeet.internal\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestDefaultNetNSResolvConfPreservesExplicitDefaultNSOptOut(t *testing.T) {
	t.Setenv("DEFAULT_NS", "9.9.9.9")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := defaultNetNSResolvConf()
	want := "nameserver 9.9.9.9\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Update generated service install expectation**

In `TestInstallerCloseStagesGeneratedPythonComposeWithNetworkArtifacts`, remove the `DEFAULT_NS` and `DEFAULT_SEARCH_DOMAINS` environment setup at the top of the test and change the resolver assertion to:

```go
	assertInstallerFileContent(t, resolvPath, "nameserver 192.168.100.1\nsearch yeet.internal\n")
```

Add a separate focused opt-out test if the removed test coverage is needed:

```go
func TestInstallerCloseStagesExplicitDefaultNSOptOut(t *testing.T) {
	t.Setenv("DEFAULT_NS", "9.9.9.9")
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "dns-optout"},
		Network:      NetworkOpts{Interfaces: "svc"},
		StageOnly:    true,
		PayloadName:  "main.py",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if _, err := installer.Write([]byte("print('hello')\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	service := testService(t, server, "dns-optout")
	resolvPath := stagedArtifactPath(t, service, db.ArtifactNetNSResolv)
	assertInstallerFileContent(t, resolvPath, "nameserver 9.9.9.9\n")
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestDefaultNetNSResolvConf|TestInstallerCloseStagesGeneratedPythonComposeWithNetworkArtifacts|TestInstallerCloseStagesExplicitDefaultNSOptOut' -count=1
```

Expected: FAIL because default resolver still uses `8.8.8.8`.

- [ ] **Step 4: Implement resolver defaults**

Modify `defaultNetNSResolvConf`:

```go
func defaultNetNSResolvConf() string {
	if v := strings.TrimSpace(os.Getenv("DEFAULT_NS")); v != "" {
		return buildNetNSResolvConf(v, os.Getenv("DEFAULT_SEARCH_DOMAINS"))
	}
	searchDomains := strings.TrimSpace(os.Getenv("DEFAULT_SEARCH_DOMAINS"))
	if searchDomains == "" {
		searchDomains = strings.TrimSuffix(yeetDNSDomain, ".")
	}
	return buildNetNSResolvConf(yeetDNSHostIP, searchDomains)
}
```

Use the `yeetDNSHostIP` constant from `pkg/catch/dns.go`; both files are in package `catch`.

- [ ] **Step 5: Run gofmt and targeted tests**

Run:

```bash
gofmt -w pkg/catch/installer_file.go pkg/catch/installer_file_test.go pkg/catch/dns.go
go test ./pkg/catch -run 'TestDefaultNetNSResolvConf|TestInstallerCloseStagesGeneratedPythonComposeWithNetworkArtifacts|TestInstallerCloseStagesExplicitDefaultNSOptOut' -count=1
```

Expected: PASS.

---

### Task 6: Change VM Resolver Metadata

**Files:**
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify: `pkg/catch/vm_network.go`
- Modify: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write failing VM DNS metadata tests**

In `pkg/catch/vm_metadata_test.go`, change `TestWriteVMMetadataFiles` expected strings so svc netplan contains:

```go
for _, want := range []string{"eth0:", "192.168.100.12/24", "gateway4: 192.168.100.254", "nameservers:", "addresses: [192.168.100.1]", "search: [yeet.internal]", "eth1:", "dhcp4: true"} {
```

Add these tests near `TestVMGuestNetworkNameserversUsesDefaultNSEnvironment`:

```go
func TestVMGuestNetworkNameserversDefaultsToYeetDNS(t *testing.T) {
	t.Setenv("DEFAULT_NS", "")

	got := vmGuestNetworkNameservers(vmGuestNetwork{Mode: "svc"})
	want := []string{"192.168.100.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %#v, want %#v", got, want)
	}
}

func TestVMGuestNetworkSearchDomainsDefaultsToYeetInternal(t *testing.T) {
	t.Setenv("DEFAULT_NS", "")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := vmGuestNetworkSearchDomains(vmGuestNetwork{Mode: "svc"})
	want := []string{"yeet.internal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("search domains = %#v, want %#v", got, want)
	}
}

func TestVMGuestNetworkSearchDomainsOptOutWithDefaultNS(t *testing.T) {
	t.Setenv("DEFAULT_NS", "1.1.1.1")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	if got := vmGuestNetworkSearchDomains(vmGuestNetwork{Mode: "svc"}); got != nil {
		t.Fatalf("search domains = %#v, want nil with DEFAULT_NS opt-out", got)
	}
}
```

In `TestWriteVMGuestMetadataFiles`, add assertions for the fast Ubuntu networkd file:

```go
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "DNS=192.168.100.1")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "Domains=yeet.internal")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "DNSDefaultRoute=false")
```

In `TestWriteVMGuestMetadataFilesUsesNixOSDriver`, add:

```go
	assertFileContains(t, filepath.Join(root, "etc", "yeet-vm", "systemd-network", "10-yeet-eth0.network"), "DNS=192.168.100.1")
	assertFileContains(t, filepath.Join(root, "etc", "yeet-vm", "systemd-network", "10-yeet-eth0.network"), "Domains=yeet.internal")
```

In `pkg/catch/vm_network_test.go`, add:

```go
func TestVMNetworkPlanMarksSvcDNSRouteOnlyWhenLANPresent(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc,lan"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	networks := plan.MetadataNetworks()
	if len(networks) != 2 {
		t.Fatalf("metadata networks = %#v, want 2", networks)
	}
	if networks[0].Mode != "svc" {
		t.Fatalf("first network mode = %q, want svc", networks[0].Mode)
	}
	if networks[0].DNSDefaultRoute == nil || *networks[0].DNSDefaultRoute {
		t.Fatalf("svc DNSDefaultRoute = %#v, want false pointer", networks[0].DNSDefaultRoute)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestVMGuestNetwork.*DNS|TestVMGuestNetwork.*Search|TestWriteVMMetadataFiles|TestWriteVMGuestMetadataFiles|TestWriteVMGuestMetadataFilesUsesNixOSDriver|TestVMNetworkPlanMarksSvcDNSRouteOnlyWhenLANPresent' -count=1
```

Expected: FAIL because VM metadata still defaults to `8.8.8.8`, does not render search domains, and has no `DNSDefaultRoute` field.

- [ ] **Step 3: Add metadata fields and renderers**

Modify `vmGuestNetwork` in `pkg/catch/vm_metadata.go`:

```go
type vmGuestNetwork struct {
	Name            string
	Mode            string
	Address         string
	Gateway         string
	DHCP            bool
	Nameservers     []string
	SearchDomains   []string
	DNSDefaultRoute *bool
}
```

Modify `renderVMNetworkdUnit` after DNS rendering:

```go
	for _, ns := range vmGuestNetworkNameservers(network) {
		fmt.Fprintf(&b, "DNS=%s\n", ns)
	}
	for _, domain := range vmGuestNetworkSearchDomains(network) {
		fmt.Fprintf(&b, "Domains=%s\n", domain)
	}
	if network.DNSDefaultRoute != nil {
		fmt.Fprintf(&b, "DNSDefaultRoute=%t\n", *network.DNSDefaultRoute)
	}
```

Modify `renderVMNetworkYAML` after nameserver addresses:

```go
			if search := vmGuestNetworkSearchDomains(net); len(search) > 0 {
				fmt.Fprintf(&b, "        search: [%s]\n", strings.Join(search, ", "))
			}
```

Replace `vmGuestNetworkNameservers` and add `vmGuestNetworkSearchDomains`:

```go
func vmGuestNetworkNameservers(network vmGuestNetwork) []string {
	if len(network.Nameservers) > 0 {
		return network.Nameservers
	}
	if network.Mode != "svc" && network.Gateway == "" {
		return nil
	}
	if dns := strings.TrimSpace(os.Getenv("DEFAULT_NS")); dns != "" {
		return splitVMGuestNameservers(dns)
	}
	return []string{yeetDNSHostIP}
}

func vmGuestNetworkSearchDomains(network vmGuestNetwork) []string {
	if len(network.SearchDomains) > 0 {
		return network.SearchDomains
	}
	if network.Mode != "svc" && network.Gateway == "" {
		return nil
	}
	if strings.TrimSpace(os.Getenv("DEFAULT_NS")) != "" {
		return splitVMGuestNameservers(os.Getenv("DEFAULT_SEARCH_DOMAINS"))
	}
	search := strings.TrimSpace(os.Getenv("DEFAULT_SEARCH_DOMAINS"))
	if search != "" {
		return splitVMGuestNameservers(search)
	}
	return []string{strings.TrimSuffix(yeetDNSDomain, ".")}
}
```

Extend `validateVMGuestNetwork`:

```go
	for _, domain := range network.SearchDomains {
		if domain == "" || strings.ContainsFunc(domain, isVMMetadataControlChar) {
			return fmt.Errorf("invalid VM guest network %s search domain %q", network.Name, domain)
		}
	}
```

- [ ] **Step 4: Mark svc DNS route-only when LAN is present**

Modify `pkg/catch/vm_network.go`:

```go
func (p vmNetworkPlan) MetadataNetworks() []vmGuestNetwork {
	out := make([]vmGuestNetwork, 0, len(p.Interfaces))
	hasLAN := p.hasMode("lan")
	for _, iface := range p.Interfaces {
		network := vmGuestNetwork{
			Name:    iface.GuestName,
			Mode:    iface.Mode,
			Address: iface.GuestIP,
			Gateway: iface.Gateway,
			DHCP:    iface.DHCP,
		}
		if iface.Mode == "svc" && hasLAN {
			routeOnly := false
			network.DNSDefaultRoute = &routeOnly
		}
		out = append(out, network)
	}
	return out
}

func (p vmNetworkPlan) hasMode(mode string) bool {
	for _, iface := range p.Interfaces {
		if iface.Mode == mode {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run gofmt and targeted tests**

Run:

```bash
gofmt -w pkg/catch/vm_metadata.go pkg/catch/vm_metadata_test.go pkg/catch/vm_network.go pkg/catch/vm_network_test.go
go test ./pkg/catch -run 'TestVMGuestNetwork.*DNS|TestVMGuestNetwork.*Search|TestWriteVMMetadataFiles|TestWriteVMGuestMetadataFiles|TestWriteVMGuestMetadataFilesUsesNixOSDriver|TestVMNetworkPlanMarksSvcDNSRouteOnlyWhenLANPresent' -count=1
```

Expected: PASS.

---

### Task 7: Add User-Facing Docs

**Files:**
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/payloads/vms.mdx`

- [ ] **Step 1: Update networking docs**

Add a short section to `website/docs/concepts/networking.mdx` near the service/private networking explanation:

```mdx
## Yeet-local DNS

Services and VMs attached to yeet private networking use the catch host as
their resolver at `192.168.100.1`. Catch answers `*.yeet.internal` names from
the service database, so a service or VM named `api` is reachable as
`api.yeet.internal`.

Yeet also configures `yeet.internal` as a search domain for `svc` networking,
so normal tools can use short names such as `api` from another service netns or
from a `svc` VM. Names outside `yeet.internal` are forwarded through the catch
host's resolver.

If a host needs to keep the previous resolver behavior, set `DEFAULT_NS` before
installing or reconfiguring the workload. An explicit `DEFAULT_NS` value opts
that workload out of yeet-local DNS defaults.
```

- [ ] **Step 2: Update VM docs**

Add this paragraph to `website/docs/payloads/vms.mdx` near the `--net=svc,lan` section:

```mdx
VMs with `--net=svc` or `--net=svc,lan` receive yeet-local DNS on their private
interface. They can resolve other private services and VMs by short name, such
as `api`, or by fully qualified name, such as `api.yeet.internal`. LAN-only VMs
use LAN DHCP DNS and do not receive yeet-local DNS by default.
```

- [ ] **Step 3: Verify docs references contain no private hostnames**

Run:

```bash
rg -n 'yeet-pve1|yeet-hetz|root@pve1|root@hetz|shayne' website/docs/concepts/networking.mdx website/docs/payloads/vms.mdx
rg -n '192\\.168\\.100\\.' website/docs/concepts/networking.mdx website/docs/payloads/vms.mdx
```

Expected: the first command has no matches. The second command has either no matches or only `192.168.100.1`.

---

### Task 8: Run Local Verification

**Files:**
- All changed Go and docs files.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./pkg/catch ./cmd/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Build binaries**

Run:

```bash
go build ./cmd/catch
go build ./cmd/yeet
```

Expected: both commands exit 0.

- [ ] **Step 4: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS. If a deterministic quality hook fails, fix the reported issue and rerun the same command.

---

### Task 9: Coordinate VM Image Compatibility And Live Smoke Tests

**Files:**
- Read-only unless an image compatibility gap is found: `/Users/shayne/code/yeet-vm-images`

- [ ] **Step 1: Inspect VM image metadata consumers**

Run from `/Users/shayne/code/yeet-vm-images`:

```bash
rg -n 'systemd-network|10-yeet|Domains|DNSDefaultRoute|/etc/yeet-vm|netplan|resolv' .
```

Expected: Ubuntu and NixOS image logic consume generated network metadata. If they already consume complete systemd-networkd or netplan files, no image rebuild is required for this yeet-only metadata change.

- [ ] **Step 2: Check live DNS bind conflicts on both real hosts**

Run:

```bash
ssh root@pve1 "ss -lunpt 'sport = :53' || true; ip -br addr show yeet0 || true"
ssh root@hetz "ss -lunpt 'sport = :53' || true; ip -br addr show yeet0 || true"
```

Expected: `yeet0` has `192.168.100.1/32`. If another service is bound to `192.168.100.1:53`, stop or reconfigure that service with the user's standing permission, then rerun the check.

- [ ] **Step 3: Install updated local-source catch on yeet-pve1 and yeet-hetz**

Use the repo's local command path:

```bash
mise exec -- go run ./cmd/yeet --host yeet-pve1 init root@pve1
mise exec -- go run ./cmd/yeet --host yeet-hetz init root@hetz
```

Expected: both source installs complete and catch restarts with the local build.

- [ ] **Step 4: Verify yeet-dns.service on both hosts**

Run:

```bash
ssh root@pve1 "systemctl is-active yeet-dns.service && ss -lunp 'sport = :53' | grep 192.168.100.1"
ssh root@hetz "systemctl is-active yeet-dns.service && ss -lunp 'sport = :53' | grep 192.168.100.1"
```

Expected: `active` and a UDP listener on `192.168.100.1:53` for both hosts.

- [ ] **Step 5: Smoke service-netns DNS on yeet-pve1**

Create or reuse two throwaway `svc` services on `yeet-pve1`:

```bash
smoke_payload="$(mktemp /tmp/yeet-dns-smoke.XXXXXX.py)"
printf 'import time\nwhile True:\n    time.sleep(3600)\n' > "$smoke_payload"
mise exec -- go run ./cmd/yeet --host yeet-pve1 run dns-smoke-a "$smoke_payload" --net=svc
mise exec -- go run ./cmd/yeet --host yeet-pve1 run dns-smoke-b "$smoke_payload" --net=svc
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-smoke-a -- getent hosts dns-smoke-b
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-smoke-a -- getent hosts dns-smoke-b.yeet.internal
```

Expected: both lookups return the private svc IP for `dns-smoke-b`.

- [ ] **Step 6: Smoke VM DNS on yeet-pve1**

Create or reuse two `svc` VMs:

```bash
mise exec -- go run ./cmd/yeet --host yeet-pve1 run dns-vm-a vm://nixos/unstable --net=svc
mise exec -- go run ./cmd/yeet --host yeet-pve1 run dns-vm-b vm://nixos/unstable --net=svc
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-vm-a -- getent hosts dns-vm-b
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-vm-a -- getent hosts dns-vm-b.yeet.internal
```

Expected: both lookups return the private svc IP for `dns-vm-b`.

- [ ] **Step 7: Smoke svc,lan routing on yeet-pve1**

Run:

```bash
mise exec -- go run ./cmd/yeet --host yeet-pve1 run dns-vm-lan vm://nixos/unstable --net=svc,lan
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-vm-lan -- resolvectl domain
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-vm-lan -- getent hosts dns-vm-a
mise exec -- go run ./cmd/yeet --host yeet-pve1 ssh dns-vm-lan -- getent hosts example.com
```

Expected: `yeet.internal` appears on the svc link, `dns-vm-a` resolves through yeet DNS, and `example.com` resolves through either LAN DNS or forwarded host DNS.

- [ ] **Step 8: Clean up smoke services if they are throwaway**

Run only for throwaway names created by this task:

```bash
mise exec -- go run ./cmd/yeet --host yeet-pve1 rm dns-smoke-a --yes
mise exec -- go run ./cmd/yeet --host yeet-pve1 rm dns-smoke-b --yes
mise exec -- go run ./cmd/yeet --host yeet-pve1 rm dns-vm-a --yes
mise exec -- go run ./cmd/yeet --host yeet-pve1 rm dns-vm-b --yes
mise exec -- go run ./cmd/yeet --host yeet-pve1 rm dns-vm-lan --yes
rm -f "${smoke_payload:-}"
```

Expected: throwaway smoke services are removed.

---

## Final Verification

Run:

```bash
go test ./pkg/catch ./cmd/catch -count=1
go test ./...
go build ./cmd/catch
go build ./cmd/yeet
pre-commit run --all-files
```

Expected: all commands pass.

Also record live evidence:

```bash
ssh root@pve1 "systemctl is-active yeet-dns.service && ss -lunp 'sport = :53' | grep 192.168.100.1"
ssh root@hetz "systemctl is-active yeet-dns.service && ss -lunp 'sport = :53' | grep 192.168.100.1"
```

Expected: `yeet-dns.service` is active and bound to `192.168.100.1:53` on both hosts.

## Self-Review

- Spec coverage: The plan covers a catch-owned miekg/dns server, forwarding, `*.yeet.internal`, short-name support through search domains, service and VM `SvcNetwork` records, no nftables/iptables in v1, `DEFAULT_NS` opt-out, no migrations, `svc,lan` DNS routing, docs, and live smoke tests on both real hosts.
- Placeholder scan: No implementation task is left as unspecified future work. The only dynamic values are GitButler change IDs, which must be read from `but status -fv` during execution.
- Type consistency: DNS code uses `db.DataView`, `db.Store.Get`, `svc.SystemdUnit`, and the existing `vmGuestNetwork`/`vmNetworkPlan` flow already used by VM provisioning.
