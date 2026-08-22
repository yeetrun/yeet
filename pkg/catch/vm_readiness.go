// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type vmGuestReadyReport struct {
	Interface string
	IP        netip.Addr
}

type vmGuestReadyBoundary struct {
	Cursor string
	Since  time.Time
}

type vmGuestReadyWaitInput struct {
	Service     string
	Network     vmNetworkPlan
	Boundary    vmGuestReadyBoundary
	VsockSocket string
}

type vmGuestReadyJournalRunner func(context.Context, []string) ([]byte, error)

type vmGuestReadyJournalStream struct {
	Lines  <-chan []byte
	Errors <-chan error
}

type vmGuestReadyJournalFollower func(context.Context, []string) (vmGuestReadyJournalStream, error)

var (
	vmGuestReadyJournalOutput     = runVMGuestReadyJournalOutput
	vmGuestReadyJournalFollow     = runVMGuestReadyJournalFollow
	vmGuestReadyNow               = time.Now
	vmGuestReadyAgentPollInterval = 25 * time.Millisecond
	vmGuestReadyTimeout           = 30 * time.Second
)

func captureVMGuestReadyBoundary(ctx context.Context, service string) (vmGuestReadyBoundary, error) {
	boundary := vmGuestReadyBoundary{Since: vmGuestReadyNow().UTC()}
	args := []string{"journalctl", "-u", vmSystemdUnitName(service), "-n", "1", "-o", "export", "--no-pager"}
	raw, err := vmGuestReadyJournalOutput(ctx, args)
	if err != nil {
		return boundary, nil
	}
	if cursor := parseJournalCursor(raw); cursor != "" {
		boundary.Cursor = cursor
	}
	return boundary, nil
}

func waitVMGuestReady(ctx context.Context, input vmGuestReadyWaitInput) (vmGuestReadyReport, error) {
	service := strings.TrimSpace(input.Service)
	allowed := vmGuestReadyInterfaces(input.Network)
	interfaceOrder := vmGuestReadyInterfaceOrder(input.Network)
	ctx, cancel := context.WithTimeout(ctx, vmGuestReadyTimeout)
	defer cancel()

	lines, journalErrors, lastErr := startVMGuestReadyJournal(ctx, service, input.Boundary)

	ticker := time.NewTicker(vmGuestReadyAgentPollInterval)
	defer ticker.Stop()
	for {
		report, ok, agentErr := readVMGuestReadyFromAgent(ctx, input.VsockSocket, interfaceOrder)
		lastErr = latestVMGuestReadyError(lastErr, agentErr)
		if ok {
			return report, nil
		}
		select {
		case line, open := <-lines:
			if !open {
				lines = nil
				continue
			}
			if report, ok := parseVMGuestReadyReport(line, allowed); ok {
				return report, nil
			}
		case err, open := <-journalErrors:
			journalErrors, lastErr = consumeVMGuestReadyJournalError(journalErrors, lastErr, err, open)
		case <-ctx.Done():
			return vmGuestReadyReport{}, vmGuestReadyTimeoutError(service, lastErr)
		case <-ticker.C:
		}
	}
}

func startVMGuestReadyJournal(ctx context.Context, service string, boundary vmGuestReadyBoundary) (<-chan []byte, <-chan error, error) {
	stream, err := vmGuestReadyJournalFollow(ctx, vmGuestReadyJournalFollowArgs(service, boundary))
	if err != nil {
		return nil, nil, fmt.Errorf("follow VM journal: %w", err)
	}
	return stream.Lines, stream.Errors, nil
}

func latestVMGuestReadyError(current, next error) error {
	if next != nil {
		return next
	}
	return current
}

func consumeVMGuestReadyJournalError(errors <-chan error, current, next error, open bool) (<-chan error, error) {
	if !open {
		return nil, current
	}
	return errors, latestVMGuestReadyError(current, next)
}

func vmGuestReadyTimeoutError(service string, lastErr error) error {
	msg := fmt.Sprintf("VM %s started, but guest readiness was not reported within %s; use `yeet vm console %s`", service, vmGuestReadyTimeout, service)
	if lastErr != nil {
		return fmt.Errorf("%s: %w", msg, lastErr)
	}
	return fmt.Errorf("%s", msg)
}

func vmGuestReadyJournalFollowArgs(service string, boundary vmGuestReadyBoundary) []string {
	args := []string{"journalctl", "-u", vmSystemdUnitName(service), "-o", "cat", "--no-pager"}
	if boundary.Cursor != "" {
		args = append(args, "--after-cursor", boundary.Cursor)
	} else if !boundary.Since.IsZero() {
		args = append(args, "--since", "@"+strconv.FormatInt(boundary.Since.Unix(), 10))
	}
	return append(args, "--follow")
}

func readVMGuestReadyFromAgent(ctx context.Context, socketPath string, interfaces []string) (vmGuestReadyReport, bool, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" || len(interfaces) == 0 {
		return vmGuestReadyReport{}, false, nil
	}
	state, err := queryVMGuestReadyFn(ctx, socketPath)
	if err != nil {
		return vmGuestReadyReport{}, false, fmt.Errorf("read VM agent readiness: %w", err)
	}
	if !state.SSHReady {
		return vmGuestReadyReport{}, false, nil
	}
	agentIPs := vmAgentInterfaceIPs(state.Network)
	for _, name := range interfaces {
		for _, raw := range agentIPs[name] {
			ip, err := netip.ParseAddr(raw)
			if err != nil {
				continue
			}
			return vmGuestReadyReport{Interface: name, IP: ip}, true, nil
		}
	}
	return vmGuestReadyReport{}, false, nil
}

func parseVMGuestReadyReport(raw []byte, allowed map[string]struct{}) (vmGuestReadyReport, bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "yeet-ready" {
			continue
		}
		if _, ok := allowed[fields[1]]; !ok {
			continue
		}
		ip, err := netip.ParseAddr(fields[2])
		if err != nil {
			continue
		}
		return vmGuestReadyReport{Interface: fields[1], IP: ip}, true
	}
	return vmGuestReadyReport{}, false
}

func vmGuestReadyInterfaceOrder(network vmNetworkPlan) []string {
	out := make([]string, 0, len(network.Interfaces))
	seen := map[string]struct{}{}
	for _, iface := range network.Interfaces {
		name := strings.TrimSpace(iface.GuestName)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func vmGuestReadyInterfaces(network vmNetworkPlan) map[string]struct{} {
	out := make(map[string]struct{}, len(network.Interfaces))
	for _, iface := range network.Interfaces {
		name := strings.TrimSpace(iface.GuestName)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

func parseJournalCursor(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if cursor, ok := strings.CutPrefix(line, "__CURSOR="); ok {
			return strings.TrimSpace(cursor)
		}
	}
	return ""
}

func runVMGuestReadyJournalOutput(ctx context.Context, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("journal command is empty")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	return cmd.Output()
}

func runVMGuestReadyJournalFollow(ctx context.Context, args []string) (vmGuestReadyJournalStream, error) {
	if len(args) == 0 {
		return vmGuestReadyJournalStream{}, fmt.Errorf("journal command is empty")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return vmGuestReadyJournalStream{}, fmt.Errorf("open journal output: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return vmGuestReadyJournalStream{}, fmt.Errorf("start journal follower: %w", err)
	}

	lines := make(chan []byte)
	errorsOut := make(chan error, 1)
	go func() {
		defer close(lines)
		defer close(errorsOut)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			select {
			case lines <- line:
			case <-ctx.Done():
				_ = cmd.Wait()
				return
			}
		}
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if err := errors.Join(scanner.Err(), waitErr); err != nil {
			if output := strings.TrimSpace(stderr.String()); output != "" {
				err = fmt.Errorf("follow VM journal: %w: %s", err, output)
			} else {
				err = fmt.Errorf("follow VM journal: %w", err)
			}
			errorsOut <- err
		}
	}()

	return vmGuestReadyJournalStream{Lines: lines, Errors: errorsOut}, nil
}
