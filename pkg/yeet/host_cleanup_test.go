// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

func TestHandleHostCleanupParsesLiteralCommand(t *testing.T) {
	state := stubHostCleanupRuntime(t)
	if err := HandleHostCleanup(context.Background(), []string{"cleanup", "--from=/root/yeet-data", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(state.client.cleanupRequests) != 1 {
		t.Fatalf("cleanup requests = %#v", state.client.cleanupRequests)
	}
	if got := state.client.cleanupRequests[0]; got.From != "/root/yeet-data" || !got.Yes {
		t.Fatalf("cleanup request = %#v", got)
	}
}

func TestHostStorageCleanupRequiresFrom(t *testing.T) {
	state := stubHostCleanupRuntime(t)
	err := runHostCleanup(context.Background(), cli.HostCleanupFlags{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("runHostCleanup error = %v, want missing --from", err)
	}
	if len(state.client.cleanupRequests) != 0 {
		t.Fatalf("cleanup requests = %#v, want none", state.client.cleanupRequests)
	}
}

func TestHostStorageCleanupRequiresYesBeforeRPCWhenNonInteractive(t *testing.T) {
	state := stubHostCleanupRuntime(t)
	state.interactive = false
	source := filepath.Join(t.TempDir(), "legacy")
	err := runHostCleanup(context.Background(), cli.HostCleanupFlags{From: source})
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), source) {
		t.Fatalf("runHostCleanup error = %v, want literal path and --yes guidance", err)
	}
	if len(state.client.cleanupRequests) != 0 {
		t.Fatalf("cleanup requests = %#v, want none", state.client.cleanupRequests)
	}
}

func TestHostStorageCleanupInteractivePromptRendersExactPath(t *testing.T) {
	state := stubHostCleanupRuntime(t)
	state.interactive = true
	source := filepath.Join(t.TempDir(), "legacy tree")
	if err := runHostCleanup(context.Background(), cli.HostCleanupFlags{From: source}); err != nil {
		t.Fatal(err)
	}
	if len(state.prompts) != 1 || !strings.Contains(state.prompts[0], source) {
		t.Fatalf("prompts = %#v, want exact path %q", state.prompts, source)
	}
	if len(state.client.cleanupRequests) != 1 || state.client.cleanupRequests[0].From != source || !state.client.cleanupRequests[0].Yes {
		t.Fatalf("cleanup requests = %#v", state.client.cleanupRequests)
	}
	for _, want := range []string{source, "Removed host storage"} {
		if !strings.Contains(state.stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", state.stdout.String(), want)
		}
	}
}

type hostCleanupTestState struct {
	client      *fakeHostStorageClient
	stdout      strings.Builder
	prompts     []string
	interactive bool
	confirm     bool
}

func stubHostCleanupRuntime(t *testing.T) *hostCleanupTestState {
	t.Helper()
	state := &hostCleanupTestState{
		client:      &fakeHostStorageClient{cleanup: catchrpc.HostStorageCleanupResult{TransactionID: "tx-1", Removed: "/root/yeet-data"}},
		interactive: true,
		confirm:     true,
	}
	oldClient := newHostStorageClientFn
	oldConfirm := confirmHostCleanupFn
	oldInteractive := hostCleanupInteractiveFn
	oldStdin := hostCleanupStdin
	oldStdout := hostCleanupStdout
	t.Cleanup(func() {
		newHostStorageClientFn = oldClient
		confirmHostCleanupFn = oldConfirm
		hostCleanupInteractiveFn = oldInteractive
		hostCleanupStdin = oldStdin
		hostCleanupStdout = oldStdout
	})
	newHostStorageClientFn = func(string) hostStorageClient { return state.client }
	confirmHostCleanupFn = func(_ io.Reader, _ io.Writer, prompt string) (bool, error) {
		state.prompts = append(state.prompts, prompt)
		return state.confirm, nil
	}
	hostCleanupInteractiveFn = func() bool { return state.interactive }
	hostCleanupStdin = strings.NewReader("")
	hostCleanupStdout = &state.stdout
	return state
}
