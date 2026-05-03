// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

type failingListHostsWriter struct {
	err error
}

func (w failingListHostsWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRenderListHosts(t *testing.T) {
	var buf bytes.Buffer
	rows := []listHostRow{
		{Host: "host-a", Version: "v0.1.0", Tags: []string{"tag:catch", "tag:app"}},
		{Host: "host-b", Version: "unknown", Tags: []string{"tag:catch"}},
	}

	if err := renderListHosts(&buf, rows); err != nil {
		t.Fatalf("renderListHosts error: %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"HOST",
		"VERSION",
		"TAGS",
		"host-a",
		"v0.1.0",
		"tag:catch,tag:app",
		"host-b",
		"unknown",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderListHosts output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderListHostsReportsFlushError(t *testing.T) {
	want := errors.New("flush failed")

	err := renderListHosts(failingListHostsWriter{err: want}, []listHostRow{{Host: "host", Version: "v0.1.0", Tags: []string{"tag:catch"}}})
	if !errors.Is(err, want) {
		t.Fatalf("renderListHosts error = %v, want %v", err, want)
	}
}

func TestHandleListHostsWritesToStdout(t *testing.T) {
	oldStatus := listHostsStatusFn
	oldInfo := listHostsCatchInfoFn
	oldStdout := os.Stdout
	defer func() {
		listHostsStatusFn = oldStatus
		listHostsCatchInfoFn = oldInfo
		os.Stdout = oldStdout
	}()

	listHostsStatusFn = func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{
			Self: &ipnstate.PeerStatus{DNSName: "self.tailnet.example."},
		}, nil
	}
	listHostsCatchInfoFn = func(context.Context, string) (serverInfo, error) {
		t.Fatal("listHostsCatchInfoFn should not be called without peers")
		return serverInfo{}, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w

	runErr := HandleListHosts(context.Background(), nil)
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("stdout close error: %v", closeErr)
	}
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll stdout error: %v", readErr)
	}
	if closeErr := r.Close(); closeErr != nil {
		t.Fatalf("stdout read close error: %v", closeErr)
	}
	if runErr != nil {
		t.Fatalf("HandleListHosts error: %v", runErr)
	}
	if !strings.Contains(string(out), "HOST") || !strings.Contains(string(out), "VERSION") {
		t.Fatalf("stdout = %q, want table header", string(out))
	}
}

func TestListHostsOverlaps(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{name: "matches", a: []string{"tag:catch", "tag:app"}, b: []string{"tag:db", "tag:catch"}, want: true},
		{name: "no match", a: []string{"tag:web"}, b: []string{"tag:db"}},
		{name: "empty", a: nil, b: []string{"tag:catch"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overlaps(tt.a, tt.b); got != tt.want {
				t.Fatalf("overlaps(%#v, %#v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestHandleListHostsFiltersStatusAndRendersRows(t *testing.T) {
	oldStatus := listHostsStatusFn
	oldInfo := listHostsCatchInfoFn
	defer func() {
		listHostsStatusFn = oldStatus
		listHostsCatchInfoFn = oldInfo
	}()

	status := &ipnstate.Status{
		Self: &ipnstate.PeerStatus{DNSName: "self.tailnet.example."},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): testListHostPeer("host-a.tailnet.example.", "tag:catch", "tag:app"),
			key.NewNode().Public(): testListHostPeer("host-b.tailnet.example.", "tag:catch"),
			key.NewNode().Public(): testListHostPeer("host-c.other.example.", "tag:catch"),
			key.NewNode().Public(): testListHostPeer("host-d.tailnet.example.", "tag:app"),
			key.NewNode().Public(): &ipnstate.PeerStatus{DNSName: "host-e.tailnet.example."},
		},
	}
	listHostsStatusFn = func(context.Context) (*ipnstate.Status, error) {
		return status, nil
	}
	var hosts []string
	listHostsCatchInfoFn = func(_ context.Context, host string) (serverInfo, error) {
		hosts = append(hosts, host)
		if host == "host-b" {
			return serverInfo{}, errors.New("offline")
		}
		return serverInfo{Version: "v0.2.3"}, nil
	}

	var out bytes.Buffer
	if err := handleListHosts(context.Background(), nil, &out); err != nil {
		t.Fatalf("handleListHosts error: %v", err)
	}

	sort.Strings(hosts)
	if got, want := strings.Join(hosts, ","), "host-a,host-b"; got != want {
		t.Fatalf("info hosts = %q, want %q", got, want)
	}
	output := out.String()
	for _, want := range []string{"host-a", "v0.2.3", "tag:catch,tag:app", "host-b", "unknown"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"host-c", "host-d", "host-e"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("output unexpectedly contains %q:\n%s", notWant, output)
		}
	}
}

func TestHandleListHostsUsesCustomTags(t *testing.T) {
	oldStatus := listHostsStatusFn
	oldInfo := listHostsCatchInfoFn
	defer func() {
		listHostsStatusFn = oldStatus
		listHostsCatchInfoFn = oldInfo
	}()

	listHostsStatusFn = func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{
			Self: &ipnstate.PeerStatus{DNSName: "self.tailnet.example."},
			Peer: map[key.NodePublic]*ipnstate.PeerStatus{
				key.NewNode().Public(): testListHostPeer("host-a.tailnet.example.", "tag:app"),
				key.NewNode().Public(): testListHostPeer("host-b.tailnet.example.", "tag:catch"),
			},
		}, nil
	}
	listHostsCatchInfoFn = func(_ context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "version-" + host}, nil
	}

	var out bytes.Buffer
	if err := handleListHosts(context.Background(), []string{"tag:app"}, &out); err != nil {
		t.Fatalf("handleListHosts error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "host-a") || strings.Contains(output, "host-b") {
		t.Fatalf("output = %q, want only custom-tagged host", output)
	}
}

func TestHandleListHostsReturnsStatusError(t *testing.T) {
	oldStatus := listHostsStatusFn
	oldInfo := listHostsCatchInfoFn
	defer func() {
		listHostsStatusFn = oldStatus
		listHostsCatchInfoFn = oldInfo
	}()

	want := errors.New("tailscale unavailable")
	listHostsStatusFn = func(context.Context) (*ipnstate.Status, error) {
		return nil, want
	}
	listHostsCatchInfoFn = func(context.Context, string) (serverInfo, error) {
		t.Fatal("listHostsCatchInfoFn should not be called after status error")
		return serverInfo{}, nil
	}

	if err := handleListHosts(context.Background(), nil, ioDiscardWriter{}); !errors.Is(err, want) {
		t.Fatalf("handleListHosts error = %v, want %v", err, want)
	}
}

func testListHostPeer(dns string, tags ...string) *ipnstate.PeerStatus {
	view := views.SliceOf(tags)
	return &ipnstate.PeerStatus{DNSName: dns, Tags: &view}
}

type ioDiscardWriter struct{}

func (ioDiscardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
