// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseVMGuestReadyReportAcceptsConfiguredInterface(t *testing.T) {
	allowed := map[string]struct{}{"eth0": {}}
	got, ok := parseVMGuestReadyReport([]byte("yeet-ready eth0 10.0.4.178\n"), allowed)
	if !ok {
		t.Fatal("parseVMGuestReadyReport ok = false, want true")
	}
	if got.Interface != "eth0" || got.IP != netip.MustParseAddr("10.0.4.178") {
		t.Fatalf("report = %#v", got)
	}
}

func TestParseVMGuestReadyReportRejectsMalformedOrUnknownInterface(t *testing.T) {
	allowed := map[string]struct{}{"eth0": {}}
	for _, raw := range []string{
		"yeet-ready eth0 not-an-ip\n",
		"yeet-ready eth9 10.0.4.178\n",
		"yeet-ip eth0 10.0.4.178\n",
	} {
		if got, ok := parseVMGuestReadyReport([]byte(raw), allowed); ok {
			t.Fatalf("parseVMGuestReadyReport(%q) = %#v, true; want false", raw, got)
		}
	}
}

func TestCaptureVMGuestReadyBoundaryUsesJournalCursor(t *testing.T) {
	stubVMGuestReadyJournal(t, func(ctx context.Context, args []string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"journalctl", "-u", "yeet-vm-devbox.service", "-n", "1", "-o", "export", "--no-pager"}) {
			t.Fatalf("args = %#v", args)
		}
		return []byte("__CURSOR=s/abc\nMESSAGE=old\n"), nil
	})

	boundary, err := captureVMGuestReadyBoundary(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("captureVMGuestReadyBoundary: %v", err)
	}
	if boundary.Cursor != "s/abc" {
		t.Fatalf("cursor = %q, want s/abc", boundary.Cursor)
	}
}

func TestCaptureVMGuestReadyBoundaryFallsBackToTimestampWhenJournalHasNoCursor(t *testing.T) {
	now := time.Unix(1234, 0).UTC()
	oldNow := vmGuestReadyNow
	vmGuestReadyNow = func() time.Time { return now }
	t.Cleanup(func() { vmGuestReadyNow = oldNow })
	stubVMGuestReadyJournal(t, func(context.Context, []string) ([]byte, error) {
		return nil, nil
	})

	boundary, err := captureVMGuestReadyBoundary(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("captureVMGuestReadyBoundary: %v", err)
	}
	if !boundary.Since.Equal(now) || boundary.Cursor != "" {
		t.Fatalf("boundary = %#v, want timestamp fallback", boundary)
	}
}

func TestWaitVMGuestReadyUsesCursorAndReturnsFreshMarker(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyPollInterval
	vmGuestReadyTimeout = time.Second
	vmGuestReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyPollInterval = oldPoll
	})
	calls := 0
	stubVMGuestReadyJournal(t, func(ctx context.Context, args []string) ([]byte, error) {
		calls++
		if !strings.Contains(strings.Join(args, " "), "--after-cursor s/abc") {
			t.Fatalf("args missing cursor: %#v", args)
		}
		if calls == 1 {
			return []byte("old boot\n"), nil
		}
		return []byte("yeet-ready eth0 10.0.4.178\n"), nil
	})

	report, err := waitVMGuestReady(context.Background(), "devbox", testVMReadyNetworkPlan(), vmGuestReadyBoundary{Cursor: "s/abc"})
	if err != nil {
		t.Fatalf("waitVMGuestReady: %v", err)
	}
	if report.Interface != "eth0" || report.IP.String() != "10.0.4.178" {
		t.Fatalf("report = %#v", report)
	}
}

func TestWaitVMGuestReadyTimeoutIncludesConsoleHint(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyPollInterval = oldPoll
	})
	stubVMGuestReadyJournal(t, func(context.Context, []string) ([]byte, error) {
		return nil, nil
	})

	_, err := waitVMGuestReady(context.Background(), "devbox", testVMReadyNetworkPlan(), vmGuestReadyBoundary{})
	if err == nil || !strings.Contains(err.Error(), "yeet vm console devbox") {
		t.Fatalf("timeout error = %v, want console hint", err)
	}
}

func TestWaitVMGuestReadyReportsJournalErrors(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyPollInterval = oldPoll
	})
	stubVMGuestReadyJournal(t, func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("journal unavailable")
	})

	_, err := waitVMGuestReady(context.Background(), "devbox", testVMReadyNetworkPlan(), vmGuestReadyBoundary{})
	if err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("waitVMGuestReady error = %v, want journal error", err)
	}
}

func testVMReadyNetworkPlan() vmNetworkPlan {
	return vmNetworkPlan{
		Service: "devbox",
		Interfaces: []vmNetworkInterfacePlan{{
			Mode:      "lan",
			GuestName: "eth0",
		}},
	}
}

func stubVMGuestReadyJournal(t *testing.T, fn vmGuestReadyJournalRunner) {
	t.Helper()
	old := vmGuestReadyJournalOutput
	vmGuestReadyJournalOutput = fn
	t.Cleanup(func() { vmGuestReadyJournalOutput = old })
}
