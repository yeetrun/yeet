// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

func TestISODNSServerServesSameFilteredResponseOverUDPAndTCP(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := tcpListener.Addr().String()
	udpConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		_ = tcpListener.Close()
		t.Fatal(err)
	}
	handler := newISODNSHandler(&testISODNSPoolStore{pool: netip.MustParsePrefix("127.0.0.0/16")}, func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Answer = []dns.RR{testISODNSA(req.Question[0].Name, "1.1.1.1"), testISODNSA(req.Question[0].Name, "10.0.0.1")}
		return resp, nil
	})
	servers := []isoDNSServer{
		&activatedISODNSTestServer{Server: &dns.Server{Listener: tcpListener, Net: "tcp", Handler: handler, ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout}},
		&activatedISODNSTestServer{Server: &dns.Server{PacketConn: udpConn, Net: "udp", Handler: handler, ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runISODNSServers(ctx, servers) }()

	for _, network := range []string{"udp", "tcp"} {
		client := &dns.Client{Net: network, Timeout: time.Second}
		resp, _, exchangeErr := client.Exchange(testISODNSQuery("example.com."), addr)
		if exchangeErr != nil {
			cancel()
			<-errCh
			t.Fatalf("%s exchange: %v", network, exchangeErr)
		}
		if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "1.1.1.1" {
			t.Fatalf("%s response = %#v, want filtered public A", network, resp.Answer)
		}
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("runISODNSServers cancellation: %v", err)
	}
}

func TestISODNSServerCancellationShutsDownBothListeners(t *testing.T) {
	first := newBlockingISODNSTestServer(nil)
	second := newBlockingISODNSTestServer(nil)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runISODNSServers(ctx, []isoDNSServer{first, second}) }()
	<-first.started
	<-second.started
	cancel()
	<-first.shutdown
	<-second.shutdown
	select {
	case err := <-errCh:
		t.Fatalf("runISODNSServers returned before listeners exited: %v", err)
	default:
	}
	close(first.allowReturn)
	close(second.allowReturn)
	if err := <-errCh; err != nil {
		t.Fatalf("runISODNSServers cancellation: %v", err)
	}
	if first.shutdownCount != 1 || second.shutdownCount != 1 {
		t.Fatalf("shutdown counts = %d, %d; want one each", first.shutdownCount, second.shutdownCount)
	}
}

func TestISODNSServerListenerFailureStopsPeerWithoutLeak(t *testing.T) {
	wantErr := errors.New("tcp bind failed")
	failed := newBlockingISODNSTestServer(wantErr)
	peer := newBlockingISODNSTestServer(nil)
	done := make(chan error, 1)
	go func() { done <- runISODNSServers(context.Background(), []isoDNSServer{peer, failed}) }()
	<-peer.shutdown
	select {
	case err := <-done:
		t.Fatalf("runISODNSServers returned before failed listener peer exited: %v", err)
	default:
	}
	close(peer.allowReturn)
	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("runISODNSServers error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runISODNSServers leaked peer after listener failure")
	}
	if peer.shutdownCount != 1 || failed.shutdownCount != 1 {
		t.Fatalf("shutdown counts = %d, %d; want one each", peer.shutdownCount, failed.shutdownCount)
	}
}

func TestISODNSServerRequiresConfigDB(t *testing.T) {
	for _, cfg := range []*Config{nil, {}} {
		if err := RunISODNSServer(context.Background(), cfg); err == nil {
			t.Fatalf("RunISODNSServer(%#v) returned nil error", cfg)
		}
	}
}

func TestRunISODNSServerBindsBothTransportsAndCancels(t *testing.T) {
	server := newISORuntimeTestServer(t, nil)
	oldAddress := isoDNSListenAddressForServer
	oldBind := bindISODNSListenersForServer
	isoDNSListenAddressForServer = "127.0.0.1:0"
	bound := make(chan struct{})
	bindISODNSListenersForServer = func(addr string, listenPacket isoDNSListenPacketFunc, listen isoDNSListenFunc) (net.PacketConn, net.Listener, error) {
		packetConn, listener, err := bindISODNSListeners(addr, listenPacket, listen)
		close(bound)
		return packetConn, listener, err
	}
	t.Cleanup(func() {
		isoDNSListenAddressForServer = oldAddress
		bindISODNSListenersForServer = oldBind
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunISODNSServer(ctx, &server.cfg) }()
	select {
	case <-bound:
	case <-time.After(time.Second):
		t.Fatal("RunISODNSServer did not bind listeners")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunISODNSServer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunISODNSServer did not stop after cancellation")
	}

	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	if err := RunISODNSServer(canceled, &server.cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunISODNSServer canceled error = %v", err)
	}
}

func TestConfigISODNSPoolStoreLoadsOnlyConfiguredPrivatePool(t *testing.T) {
	valid := (&db.Data{ISOPool: &db.ISOPool{
		Prefix:           netip.MustParsePrefix("172.30.0.0/16"),
		AllocatorVersion: iso.AllocatorVersion,
		PolicyVersion:    iso.PolicyVersion,
	}}).View()
	store := configISODNSPoolStore{store: fakeDNSStore{view: valid}}
	pool, err := store.ISOPool(context.Background())
	if err != nil || pool != netip.MustParsePrefix("172.30.0.0/16") {
		t.Fatalf("ISOPool = %v, %v", pool, err)
	}

	wantErr := errors.New("database unavailable")
	for _, tt := range []struct {
		name  string
		store dnsDataStore
	}{
		{name: "missing store"},
		{name: "load failure", store: fakeDNSStore{err: wantErr}},
		{name: "missing pool", store: fakeDNSStore{view: (&db.Data{}).View()}},
		{name: "public pool", store: fakeDNSStore{view: (&db.Data{ISOPool: &db.ISOPool{Prefix: netip.MustParsePrefix("198.51.100.0/24")}}).View()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (configISODNSPoolStore{store: tt.store}).ISOPool(context.Background())
			if err == nil {
				t.Fatal("ISOPool returned nil error")
			}
		})
	}
}

func TestNewConfigISODNSPoolStoreUsesConfigDatabase(t *testing.T) {
	server := newISORuntimeTestServer(t, nil)
	store := newConfigISODNSPoolStore(&server.cfg)
	pool, err := store.ISOPool(context.Background())
	if err != nil || pool != netip.MustParsePrefix("172.30.0.0/16") {
		t.Fatalf("ISOPool = %v, %v", pool, err)
	}
}

func TestISODNSServerRejectsInvalidServerSetsAndListenerErrors(t *testing.T) {
	for _, servers := range [][]isoDNSServer{nil, {newBlockingISODNSTestServer(nil)}, {nil, newBlockingISODNSTestServer(nil)}} {
		if err := runISODNSServers(context.Background(), servers); err == nil {
			t.Fatalf("runISODNSServers(%#v) returned nil error", servers)
		}
	}
	wantErr := errors.New("udp unavailable")
	udp, tcp, err := bindISODNSListeners("127.0.0.1:5353", func(string, string) (net.PacketConn, error) {
		return nil, wantErr
	}, func(string, string) (net.Listener, error) {
		t.Fatal("TCP listener called after UDP failure")
		return nil, nil
	})
	if !errors.Is(err, wantErr) || udp != nil || tcp != nil {
		t.Fatalf("bindISODNSListeners = %#v %#v %v", udp, tcp, err)
	}
	if got := ignoreClosedISODNSError(wantErr); !errors.Is(got, wantErr) {
		t.Fatalf("ignoreClosedISODNSError = %v", got)
	}
}

func TestWaitForISODNSServerExitsHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForISODNSServerExits(ctx, make(chan error), 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForISODNSServerExits error = %v", err)
	}
}

func TestISODNSServerPartialPrebindFailureClosesUDPListener(t *testing.T) {
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackingISODNSPacketConn{PacketConn: packetConn}
	wantErr := errors.New("tcp unavailable")
	udp, tcp, err := bindISODNSListeners(
		"127.0.0.1:5353",
		func(network, address string) (net.PacketConn, error) {
			if network != "udp" || address != "127.0.0.1:5353" {
				t.Fatalf("packet listen = %q %q", network, address)
			}
			return tracked, nil
		},
		func(network, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:5353" {
				t.Fatalf("stream listen = %q %q", network, address)
			}
			return nil, wantErr
		},
	)
	if !errors.Is(err, wantErr) || udp != nil || tcp != nil {
		t.Fatalf("bindISODNSListeners = %#v %#v %v, want nil listeners and %v", udp, tcp, err, wantErr)
	}
	if !tracked.closed {
		t.Fatal("UDP listener remained open after TCP pre-bind failure")
	}
}

func TestISODNSServerShutdownBeforeActivateClosesPreboundSocket(t *testing.T) {
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackingISODNSPacketConn{PacketConn: packetConn}
	server := &activatedISODNSServer{Server: &dns.Server{PacketConn: tracked, Net: "udp", Handler: dns.HandlerFunc(func(dns.ResponseWriter, *dns.Msg) {})}}
	if err := server.ShutdownContext(context.Background()); err != nil {
		t.Fatalf("ShutdownContext before activate: %v", err)
	}
	if !tracked.closed {
		t.Fatal("prebound socket remained open when shutdown raced ahead of activation")
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ActivateAndServe blocked on socket closed by pre-start shutdown")
	}
}

type activatedISODNSTestServer struct{ *dns.Server }

func (s *activatedISODNSTestServer) ListenAndServe() error { return s.ActivateAndServe() }

type blockingISODNSTestServer struct {
	started       chan struct{}
	stopped       chan struct{}
	shutdown      chan struct{}
	allowReturn   chan struct{}
	result        error
	shutdownOnce  sync.Once
	shutdownCount int
}

func newBlockingISODNSTestServer(result error) *blockingISODNSTestServer {
	return &blockingISODNSTestServer{
		started: make(chan struct{}), stopped: make(chan struct{}), shutdown: make(chan struct{}), allowReturn: make(chan struct{}), result: result,
	}
}

func (s *blockingISODNSTestServer) ListenAndServe() error {
	close(s.started)
	if s.result != nil {
		return s.result
	}
	<-s.stopped
	<-s.allowReturn
	return nil
}

func (s *blockingISODNSTestServer) ShutdownContext(context.Context) error {
	s.shutdownOnce.Do(func() {
		s.shutdownCount++
		close(s.stopped)
		close(s.shutdown)
	})
	return nil
}

type trackingISODNSPacketConn struct {
	net.PacketConn
	closed bool
}

func (c *trackingISODNSPacketConn) Close() error {
	c.closed = true
	return c.PacketConn.Close()
}
