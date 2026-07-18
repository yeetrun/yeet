// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const isoDNSListenAddr = "0.0.0.0:5353"

var (
	isoDNSListenAddressForServer = isoDNSListenAddr
	bindISODNSListenersForServer = bindISODNSListeners
)

type isoDNSServer interface {
	ListenAndServe() error
	ShutdownContext(context.Context) error
}

func RunISODNSServer(ctx context.Context, cfg *Config) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("ISO DNS server requires a DB")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handler := newISODNSHandler(newConfigISODNSPoolStore(cfg), nil)
	packetConn, listener, err := bindISODNSListenersForServer(isoDNSListenAddressForServer, net.ListenPacket, net.Listen)
	if err != nil {
		return err
	}
	servers := []isoDNSServer{
		&activatedISODNSServer{Server: &dns.Server{PacketConn: packetConn, Net: "udp", Handler: handler, ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout}},
		&activatedISODNSServer{Server: &dns.Server{Listener: listener, Net: "tcp", Handler: handler, ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout}},
	}
	return runISODNSServers(ctx, servers)
}

type activatedISODNSServer struct{ *dns.Server }

func (s *activatedISODNSServer) ListenAndServe() error {
	return s.ActivateAndServe()
}

func (s *activatedISODNSServer) ShutdownContext(ctx context.Context) error {
	err := s.Server.ShutdownContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "server not started") {
		return err
	}
	var closeErr error
	if s.PacketConn != nil {
		closeErr = errors.Join(closeErr, ignoreClosedISODNSError(s.PacketConn.Close()))
	}
	if s.Listener != nil {
		closeErr = errors.Join(closeErr, ignoreClosedISODNSError(s.Listener.Close()))
	}
	return closeErr
}

func ignoreClosedISODNSError(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type isoDNSListenPacketFunc func(network, address string) (net.PacketConn, error)
type isoDNSListenFunc func(network, address string) (net.Listener, error)

func bindISODNSListeners(addr string, listenPacket isoDNSListenPacketFunc, listen isoDNSListenFunc) (net.PacketConn, net.Listener, error) {
	packetConn, err := listenPacket("udp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("bind ISO DNS UDP listener %s: %w", addr, err)
	}
	listener, err := listen("tcp", addr)
	if err != nil {
		closeErr := packetConn.Close()
		return nil, nil, errors.Join(fmt.Errorf("bind ISO DNS TCP listener %s: %w", addr, err), closeErr)
	}
	return packetConn, listener, nil
}

func runISODNSServers(ctx context.Context, servers []isoDNSServer) error {
	if len(servers) != 2 || servers[0] == nil || servers[1] == nil {
		return fmt.Errorf("ISO DNS requires exactly one UDP and one TCP server")
	}
	errCh := startISODNSServers(servers)
	received, serveErr := waitForISODNSServerStop(ctx, errCh)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownErr := shutdownISODNSServers(shutdownCtx, servers)
	joinErr := waitForISODNSServerExits(shutdownCtx, errCh, received, len(servers))
	if serveErr != nil {
		log.Printf("ISO DNS server stopped after listener failure: %v", serveErr)
	}
	return errors.Join(serveErr, shutdownErr, joinErr)
}

func startISODNSServers(servers []isoDNSServer) <-chan error {
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func(server isoDNSServer) {
			errCh <- server.ListenAndServe()
		}(server)
	}
	return errCh
}

func waitForISODNSServerStop(ctx context.Context, errCh <-chan error) (int, error) {
	select {
	case <-ctx.Done():
		return 0, nil
	case err := <-errCh:
		if err == nil && ctx.Err() == nil {
			err = fmt.Errorf("ISO DNS listener stopped unexpectedly")
		}
		return 1, err
	}
}

func shutdownISODNSServers(ctx context.Context, servers []isoDNSServer) error {
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func(server isoDNSServer) {
			errCh <- server.ShutdownContext(ctx)
		}(server)
	}
	var result error
	for range servers {
		result = errors.Join(result, <-errCh)
	}
	return result
}

func waitForISODNSServerExits(ctx context.Context, errCh <-chan error, received, total int) error {
	for received < total {
		select {
		case <-errCh:
			received++
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for ISO DNS listeners to exit: %w", ctx.Err())
		}
	}
	return nil
}

type configISODNSPoolStore struct {
	store dnsDataStore
}

func newConfigISODNSPoolStore(cfg *Config) isoDNSPoolStore {
	return configISODNSPoolStore{store: dnsStoreForConfig(cfg)}
}

func (s configISODNSPoolStore) ISOPool(context.Context) (netip.Prefix, error) {
	if s.store == nil {
		return netip.Prefix{}, fmt.Errorf("ISO DNS pool store is unavailable")
	}
	dv, err := s.store.Get()
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("load ISO DNS pool: %w", err)
	}
	pool := dv.ISOPool()
	if !pool.Valid() || !validISODNSPool(pool.Prefix()) {
		return netip.Prefix{}, fmt.Errorf("ISO DNS pool is not configured")
	}
	return pool.Prefix(), nil
}
