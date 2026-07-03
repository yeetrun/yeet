// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

type vmSSHProxyDialer func(context.Context, string, string) (net.Conn, error)

var vmSSHProxyDialFunc vmSSHProxyDialer = func(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

func (e *ttyExecer) vmSSHProxyCmdFunc(args []string) error {
	host, port, err := e.vmSSHProxyTarget(args)
	if err != nil {
		return err
	}
	rw := e.rw
	if rw == nil {
		rw = e.rawRW
	}
	if rw == nil {
		return fmt.Errorf("VM SSH proxy stream is unavailable")
	}
	conn, err := vmSSHProxyDialFunc(e.ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("dial VM SSH %s:%s: %w", host, port, err)
	}
	return proxyVMSSHConn(rw, conn)
}

func (e *ttyExecer) vmSSHProxyTarget(args []string) (string, string, error) {
	if len(args) != 2 {
		return "", "", fmt.Errorf("expected VM SSH proxy host and port")
	}
	host := strings.TrimSpace(args[0])
	port := strings.TrimSpace(args[1])
	if host == "" || port == "" {
		return "", "", fmt.Errorf("expected VM SSH proxy host and port")
	}
	if port != "22" {
		return "", "", fmt.Errorf("VM SSH proxy only supports port 22")
	}
	resp, err := e.s.serviceInfoWithContext(e.ctx, e.sn)
	if err != nil {
		return "", "", err
	}
	if !resp.Found {
		return "", "", fmt.Errorf("service %q not found", e.sn)
	}
	if resp.Info.ServiceType != string(db.ServiceTypeVM) {
		return "", "", fmt.Errorf("service %q is not a VM service", e.sn)
	}
	allowed := vmSSHProxyAllowedHosts(resp.Info)
	if !slices.Contains(allowed, host) {
		return "", "", fmt.Errorf("VM SSH proxy target %s does not match VM SSH address for %q", host, e.sn)
	}
	return host, port, nil
}

func vmSSHProxyAllowedHosts(info catchrpc.ServiceInfo) []string {
	var out []string
	add := func(host string) {
		host = strings.TrimSpace(host)
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	add(info.Network.SvcIP)
	if info.VM != nil {
		if info.VM.SSH != nil {
			add(info.VM.SSH.Host)
		}
		for _, network := range info.VM.Networks {
			add(network.IP)
		}
	}
	return out
}

func proxyVMSSHConn(rw io.ReadWriter, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	type copyResult struct {
		remoteToLocal bool
		err           error
	}
	errCh := make(chan copyResult, 2)
	go func() {
		_, err := io.Copy(conn, rw)
		if closeWriter, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = closeWriter.CloseWrite()
		}
		errCh <- copyResult{err: err}
	}()
	go func() {
		_, err := io.Copy(rw, conn)
		errCh <- copyResult{remoteToLocal: true, err: err}
	}()

	first := <-errCh
	if first.remoteToLocal {
		_ = conn.Close()
	}
	second := <-errCh
	return expectedCopyErrorsOnly(first.err, second.err)
}

func expectedCopyErrorsOnly(errs ...error) error {
	var unexpected []error
	for _, err := range errs {
		if err == nil || isExpectedCopyErr(err) {
			continue
		}
		unexpected = append(unexpected, err)
	}
	return errors.Join(unexpected...)
}
