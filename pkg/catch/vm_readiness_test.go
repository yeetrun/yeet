// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
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

func TestVMGuestReadyDefaultTimeoutIsThirtySeconds(t *testing.T) {
	if vmGuestReadyTimeout != 30*time.Second {
		t.Fatalf("vmGuestReadyTimeout = %s, want 30s", vmGuestReadyTimeout)
	}
}

func TestWaitVMGuestReadyFollowsCursorOnce(t *testing.T) {
	lines := make(chan []byte, 2)
	errs := make(chan error, 1)
	lines <- []byte("old boot")
	lines <- []byte("yeet-ready eth0 10.0.4.178")

	calls := 0
	stubVMGuestReadyJournalFollow(t, func(ctx context.Context, args []string) (vmGuestReadyJournalStream, error) {
		calls++
		want := []string{"journalctl", "-u", "yeet-vm-devbox.service", "-o", "cat", "--no-pager", "--after-cursor", "s/abc", "--follow"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
		return vmGuestReadyJournalStream{Lines: lines, Errors: errs}, nil
	})

	report, err := waitVMGuestReady(context.Background(), vmGuestReadyWaitInput{
		Service:  "devbox",
		Network:  testVMReadyNetworkPlan(),
		Boundary: vmGuestReadyBoundary{Cursor: "s/abc"},
	})
	if err != nil {
		t.Fatalf("waitVMGuestReady: %v", err)
	}
	if calls != 1 {
		t.Fatalf("journal follower calls = %d, want 1", calls)
	}
	if report.Interface != "eth0" || report.IP.String() != "10.0.4.178" {
		t.Fatalf("report = %#v", report)
	}
}

func TestVMGuestReadyJournalFollowArgsUsesTimestampFallback(t *testing.T) {
	boundary := vmGuestReadyBoundary{Since: time.Unix(1234, 0).UTC()}
	want := []string{"journalctl", "-u", "yeet-vm-devbox.service", "-o", "cat", "--no-pager", "--since", "@1234", "--follow"}
	if got := vmGuestReadyJournalFollowArgs("devbox", boundary); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestWaitVMGuestReadyReturnsAgentReadinessWhenJournalHasNoMarker(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyAgentPollInterval
	vmGuestReadyTimeout = time.Second
	vmGuestReadyAgentPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyAgentPollInterval = oldPoll
	})
	stubIdleVMGuestReadyJournalFollow(t)
	oldQuery := queryVMGuestReadyFn
	queryVMGuestReadyFn = func(ctx context.Context, socketPath string) (vmAgentGuestReadyState, error) {
		if socketPath != "/run/devbox/vsock.sock" {
			t.Fatalf("socketPath = %q, want /run/devbox/vsock.sock", socketPath)
		}
		return vmAgentGuestReadyState{
			Network: vmAgentNetworkState{Interfaces: []vmAgentInterface{{
				Name: "eth0",
				Up:   true,
				IPs:  []string{"10.0.4.178"},
			}}},
			SSHReady: true,
		}, nil
	}
	t.Cleanup(func() { queryVMGuestReadyFn = oldQuery })

	report, err := waitVMGuestReady(context.Background(), vmGuestReadyWaitInput{
		Service:     "devbox",
		Network:     testVMReadyNetworkPlan(),
		VsockSocket: "/run/devbox/vsock.sock",
	})
	if err != nil {
		t.Fatalf("waitVMGuestReady: %v", err)
	}
	if report.Interface != "eth0" || report.IP.String() != "10.0.4.178" {
		t.Fatalf("report = %#v", report)
	}
}

func TestWaitVMGuestReadyWaitsWhenAgentSSHIsNotReady(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyAgentPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyAgentPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyAgentPollInterval = oldPoll
	})
	stubIdleVMGuestReadyJournalFollow(t)
	oldQuery := queryVMGuestReadyFn
	queryVMGuestReadyFn = func(ctx context.Context, socketPath string) (vmAgentGuestReadyState, error) {
		return vmAgentGuestReadyState{
			Network: vmAgentNetworkState{Interfaces: []vmAgentInterface{{
				Name: "eth0",
				Up:   true,
				IPs:  []string{"10.0.4.178"},
			}}},
			SSHReady: false,
		}, nil
	}
	t.Cleanup(func() { queryVMGuestReadyFn = oldQuery })

	_, err := waitVMGuestReady(context.Background(), vmGuestReadyWaitInput{
		Service:     "devbox",
		Network:     testVMReadyNetworkPlan(),
		VsockSocket: "/run/devbox/vsock.sock",
	})
	if err == nil || !strings.Contains(err.Error(), "yeet vm console devbox") {
		t.Fatalf("waitVMGuestReady error = %v, want timeout with console hint", err)
	}
}

func TestWaitVMGuestReadyIgnoresAgentInterfacesOutsidePlan(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyAgentPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyAgentPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyAgentPollInterval = oldPoll
	})
	stubIdleVMGuestReadyJournalFollow(t)
	oldQuery := queryVMGuestReadyFn
	queryVMGuestReadyFn = func(ctx context.Context, socketPath string) (vmAgentGuestReadyState, error) {
		return vmAgentGuestReadyState{
			Network: vmAgentNetworkState{Interfaces: []vmAgentInterface{{
				Name: "eth9",
				Up:   true,
				IPs:  []string{"10.0.4.178"},
			}}},
			SSHReady: true,
		}, nil
	}
	t.Cleanup(func() { queryVMGuestReadyFn = oldQuery })

	_, err := waitVMGuestReady(context.Background(), vmGuestReadyWaitInput{
		Service:     "devbox",
		Network:     testVMReadyNetworkPlan(),
		VsockSocket: "/run/devbox/vsock.sock",
	})
	if err == nil || !strings.Contains(err.Error(), "yeet vm console devbox") {
		t.Fatalf("waitVMGuestReady error = %v, want timeout with console hint", err)
	}
}

func TestWaitVMGuestReadyTimeoutIncludesConsoleHint(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyAgentPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyAgentPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyAgentPollInterval = oldPoll
	})
	stubIdleVMGuestReadyJournalFollow(t)

	_, err := waitVMGuestReady(context.Background(), vmGuestReadyWaitInput{
		Service: "devbox",
		Network: testVMReadyNetworkPlan(),
	})
	if err == nil || !strings.Contains(err.Error(), "yeet vm console devbox") {
		t.Fatalf("timeout error = %v, want console hint", err)
	}
}

func TestWaitVMGuestReadyReportsJournalErrors(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyAgentPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyAgentPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyAgentPollInterval = oldPoll
	})
	stubVMGuestReadyJournalFollow(t, func(context.Context, []string) (vmGuestReadyJournalStream, error) {
		return vmGuestReadyJournalStream{}, errors.New("journal unavailable")
	})

	_, err := waitVMGuestReady(context.Background(), vmGuestReadyWaitInput{
		Service: "devbox",
		Network: testVMReadyNetworkPlan(),
	})
	if err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("waitVMGuestReady error = %v, want journal error", err)
	}
}

func TestRunVMGuestReadyJournalFollowStreamsLines(t *testing.T) {
	script := writeVMGuestReadyJournalScript(t, "printf 'first\\nsecond\\n'")
	stream, err := runVMGuestReadyJournalFollow(context.Background(), []string{script})
	if err != nil {
		t.Fatalf("runVMGuestReadyJournalFollow: %v", err)
	}

	var lines []string
	for line := range stream.Lines {
		lines = append(lines, string(line))
	}
	if !reflect.DeepEqual(lines, []string{"first", "second"}) {
		t.Fatalf("lines = %#v", lines)
	}
	if err, ok := <-stream.Errors; ok {
		t.Fatalf("unexpected follower error: %v", err)
	}
}

func TestRunVMGuestReadyJournalFollowReportsProcessError(t *testing.T) {
	script := writeVMGuestReadyJournalScript(t, "printf 'journal failed\\n' >&2\nexit 7")
	stream, err := runVMGuestReadyJournalFollow(context.Background(), []string{script})
	if err != nil {
		t.Fatalf("runVMGuestReadyJournalFollow: %v", err)
	}
	for range stream.Lines {
	}
	got := <-stream.Errors
	if got == nil || !strings.Contains(got.Error(), "exit status 7") || !strings.Contains(got.Error(), "journal failed") {
		t.Fatalf("follower error = %v", got)
	}
}

func TestRunVMGuestReadyJournalFollowRejectsInvalidCommand(t *testing.T) {
	if _, err := runVMGuestReadyJournalFollow(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty command error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing-journalctl")
	if _, err := runVMGuestReadyJournalFollow(context.Background(), []string{missing}); err == nil || !strings.Contains(err.Error(), "start journal follower") {
		t.Fatalf("missing command error = %v", err)
	}
}

func TestRunVMGuestReadyJournalFollowStopsOnCancellation(t *testing.T) {
	script := writeVMGuestReadyJournalScript(t, "printf 'started\\n'\nexec sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := runVMGuestReadyJournalFollow(ctx, []string{script})
	if err != nil {
		t.Fatalf("runVMGuestReadyJournalFollow: %v", err)
	}
	if got := <-stream.Lines; string(got) != "started" {
		t.Fatalf("first line = %q", got)
	}
	cancel()

	select {
	case _, ok := <-stream.Lines:
		if ok {
			t.Fatal("lines remained open after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("journal follower did not stop after cancellation")
	}
	if err, ok := <-stream.Errors; ok {
		t.Fatalf("unexpected cancellation error: %v", err)
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

func stubVMGuestReadyJournalFollow(t *testing.T, fn vmGuestReadyJournalFollower) {
	t.Helper()
	old := vmGuestReadyJournalFollow
	vmGuestReadyJournalFollow = fn
	t.Cleanup(func() { vmGuestReadyJournalFollow = old })
}

func stubIdleVMGuestReadyJournalFollow(t *testing.T) {
	t.Helper()
	lines := make(chan []byte)
	errs := make(chan error)
	stubVMGuestReadyJournalFollow(t, func(context.Context, []string) (vmGuestReadyJournalStream, error) {
		return vmGuestReadyJournalStream{Lines: lines, Errors: errs}, nil
	})
}

func writeVMGuestReadyJournalScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journalctl")
	content := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write journal helper: %v", err)
	}
	return path
}
