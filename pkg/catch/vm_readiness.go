// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
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

type vmGuestReadyJournalRunner func(context.Context, []string) ([]byte, error)

var (
	vmGuestReadyJournalOutput = runVMGuestReadyJournalOutput
	vmGuestReadyNow           = time.Now
	vmGuestReadyPollInterval  = 500 * time.Millisecond
	vmGuestReadyTimeout       = 60 * time.Second
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

func waitVMGuestReady(ctx context.Context, service string, network vmNetworkPlan, boundary vmGuestReadyBoundary) (vmGuestReadyReport, error) {
	allowed := vmGuestReadyInterfaces(network)
	ctx, cancel := context.WithTimeout(ctx, vmGuestReadyTimeout)
	defer cancel()
	var lastErr error
	for {
		report, ok, err := readVMGuestReady(ctx, service, boundary, allowed)
		if err != nil {
			lastErr = err
		} else if ok {
			return report, nil
		}
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("VM %s started, but guest readiness was not reported within %s; use `yeet vm console %s`", service, vmGuestReadyTimeout, service)
			if lastErr != nil {
				return vmGuestReadyReport{}, fmt.Errorf("%s: %w", msg, lastErr)
			}
			return vmGuestReadyReport{}, fmt.Errorf("%s", msg)
		case <-time.After(vmGuestReadyPollInterval):
		}
	}
}

func readVMGuestReady(ctx context.Context, service string, boundary vmGuestReadyBoundary, allowed map[string]struct{}) (vmGuestReadyReport, bool, error) {
	args := []string{"journalctl", "-u", vmSystemdUnitName(service), "-o", "cat", "--no-pager"}
	if boundary.Cursor != "" {
		args = append(args, "--after-cursor", boundary.Cursor)
	} else if !boundary.Since.IsZero() {
		args = append(args, "--since", "@"+strconv.FormatInt(boundary.Since.Unix(), 10))
	}
	raw, err := vmGuestReadyJournalOutput(ctx, args)
	if err != nil {
		return vmGuestReadyReport{}, false, fmt.Errorf("read VM journal: %w", err)
	}
	report, ok := parseVMGuestReadyReport(raw, allowed)
	return report, ok, nil
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
