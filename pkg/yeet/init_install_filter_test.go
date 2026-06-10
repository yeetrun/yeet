// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type initInstallErrWriter struct {
	err error
}

func (w initInstallErrWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestInitInstallFilterSummaryAndOutput(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	input := strings.Join([]string{
		`2026/01/02 18:15:34 data dir: /root/data`,
		`2026/01/02 18:15:34 tsnet running state path /root/data/tsnet/tailscaled.state`,
		`2026/01/02 18:15:35 skipping image "plain-df"`,
		`2026/01/02 18:15:35 skipping image "nginx"`,
		`2026/01/02 18:15:35 copying /root/data/services/catch/bin/catch-123 to /root/data/services/catch/run/catch`,
		`Warning: docker is required but not installed`,
		`2026/01/02 18:15:36 Service "catch" installed`,
	}, "\n") + "\n"

	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if gotOutput := strings.TrimSpace(buf.String()); gotOutput != "" {
		t.Fatalf("unexpected output: %q", gotOutput)
	}

	wantDetail := "systemd data=/root/data tsnet=/root/data/tsnet skipped-images=2"
	if detail := filter.SummaryDetail(); detail != wantDetail {
		t.Fatalf("unexpected detail: %q", detail)
	}
	if warning := filter.WarningSummary(); warning != "Warning: docker is required but not installed" {
		t.Fatalf("unexpected warning summary: %q", warning)
	}
	if info := filter.InfoSummary(); info != "" {
		t.Fatalf("unexpected info summary: %q", info)
	}
}

func TestInitInstallFilterSummarizesVisibleWarningsOnce(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	input := strings.Join([]string{
		"Warning: VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See https://yeetrun.com/docs/getting-started/installation#vm-host-requirements",
		"Warning: VM tools are incomplete: missing qemu-img. Install packages: qemu-utils. See https://yeetrun.com/docs/getting-started/installation#vm-host-requirements",
	}, "\n") + "\n"

	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if got := buf.String(); got != "" {
		t.Fatalf("visible output = %q, want warnings deferred to summary", got)
	}
	summary := filter.WarningSummary()
	if strings.Count(summary, "Warning:") != 1 {
		t.Fatalf("WarningSummary = %q, want one Warning prefix", summary)
	}
	if strings.Count(summary, "https://yeetrun.com/docs/getting-started/installation#vm-host-requirements") != 1 {
		t.Fatalf("WarningSummary = %q, want one docs link", summary)
	}
	for _, want := range []string{
		"Warning:\n- VM support is unavailable on this host",
		"\n- VM tools are incomplete",
		"\nDocs: https://yeetrun.com/docs/getting-started/installation#vm-host-requirements",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("WarningSummary = %q, missing %q", summary, want)
		}
	}
}

func TestInitInstallFilterFlushesPrompt(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	prompt := "Would you like to install docker? [y/N]: "
	if _, err := filter.Write([]byte(prompt)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if got := buf.String(); got != prompt {
		t.Fatalf("expected prompt to pass through, got %q", got)
	}
}

func TestInitInstallFilterReportsOutputErrors(t *testing.T) {
	want := errors.New("write failed")
	filter := newInitInstallFilter(initInstallErrWriter{err: want})

	_, err := filter.Write([]byte("Error: install failed\n"))
	if !errors.Is(err, want) {
		t.Fatalf("Write error = %v, want %v", err, want)
	}
}

func TestInitInstallFilterRedactsTailscaleAuthKeys(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	input := "Error: TS_AUTHKEY=tskey-auth-secret123 TS_CLIENT_SECRET=tskey-client-secret456 failed\n"
	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	got := buf.String()
	if strings.Contains(got, "tskey-auth-secret123") || strings.Contains(got, "tskey-client-secret456") {
		t.Fatalf("filter output leaked tailscale key: %q", got)
	}
	if !strings.Contains(got, "TS_AUTHKEY=[tailscale-key-redacted] TS_CLIENT_SECRET=[tailscale-key-redacted] failed") {
		t.Fatalf("filter output = %q, want redacted keys", got)
	}
}

func TestInitInstallFilterCapturesTailscaleOAuthError(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	input := "tailscale OAuth setup failed: tag:catch is not allowed by tagOwners\n"
	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	err := filter.ErrorSummary()
	if !errors.Is(err, errTailscaleOAuthRejected) {
		t.Fatalf("ErrorSummary = %v, want errTailscaleOAuthRejected", err)
	}
	if !strings.Contains(err.Error(), "tag:catch") {
		t.Fatalf("ErrorSummary missing tag detail: %v", err)
	}
	if !strings.Contains(buf.String(), "tailscale OAuth setup failed") {
		t.Fatalf("visible output = %q, want OAuth error", buf.String())
	}
}

func TestInitInstallFilterCapturesTailscaleCredentialRequired(t *testing.T) {
	filter := newInitInstallFilter(io.Discard)

	input := "catch Tailscale setup requires a Tailscale OAuth client secret or auth key; see https://yeetrun.com/docs/concepts/tailscale\n"
	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	err := filter.ErrorSummary()
	if !errors.Is(err, errTailscaleCredentialRequired) {
		t.Fatalf("ErrorSummary = %v, want errTailscaleCredentialRequired", err)
	}
}

func TestInitInstallFilterSurfacesTailscaleSetupGuidance(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	input := "2026/01/02 18:15:34 catch Tailscale node must be tagged; configure tagOwners and rerun yeet init, or use --ts-auth-key=<key> for unattended installs; docs: https://yeetrun.com/docs/concepts/tailscale\n"
	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	got := buf.String()
	for _, want := range []string{"tagOwners", "rerun yeet init", "--ts-auth-key=<key>", "https://yeetrun.com/docs/concepts/tailscale"} {
		if !strings.Contains(got, want) {
			t.Fatalf("filter output missing %q:\n%s", want, got)
		}
	}
}

func TestInitInstallSummaryAbsorbPathsImagesAndWarnings(t *testing.T) {
	var summary initInstallSummary

	absorbed := []string{
		"data dir: /srv/yeet",
		`tsnet starting with hostname "catch" varRoot "/srv/yeet/tsnet"`,
		`skipping image "nginx"`,
		`Installation of docker skipped`,
	}
	for _, msg := range absorbed {
		if !summary.Absorb(msg) {
			t.Fatalf("expected %q to be absorbed", msg)
		}
	}

	if summary.Detail() != "systemd data=/srv/yeet tsnet=/srv/yeet/tsnet skipped-images=1" {
		t.Fatalf("detail = %q", summary.Detail())
	}
	if summary.WarningSummary() != "Warning: Installation of docker skipped" {
		t.Fatalf("warning = %q", summary.WarningSummary())
	}
}

func TestInitInstallSummaryVisibleWarnings(t *testing.T) {
	var summary initInstallSummary

	if !summary.Absorb("Warning: docker is required but not installed") {
		t.Fatal("expected warning to be absorbed into summary")
	}
	if summary.Absorb("Failed to install service: exit status 1") {
		t.Fatal("expected install failure to remain visible")
	}

	want := "Warning: docker is required but not installed"
	if got := summary.WarningSummary(); got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

func TestInitInstallSummaryInfoSummaryDedupes(t *testing.T) {
	summary := initInstallSummary{
		infos: []string{"next step", "next step", " "},
	}
	if got := summary.InfoSummary(); got != "next step" {
		t.Fatalf("InfoSummary = %q, want next step", got)
	}
}

func TestInitInstallSummaryAbsorbsVMToolInstallInfo(t *testing.T) {
	var summary initInstallSummary

	if !summary.Absorb("Installed VM host packages: qemu-utils") {
		t.Fatal("VM host package install info should be absorbed into summary")
	}
	if got := summary.InfoSummary(); got != "Installed VM host packages: qemu-utils" {
		t.Fatalf("InfoSummary = %q, want VM package install info", got)
	}
	if got := summary.WarningSummary(); got != "" {
		t.Fatalf("WarningSummary = %q, want no warning", got)
	}
}

func TestInitInstallLineClassifiers(t *testing.T) {
	ignored := []string{
		"NetNS: created",
		"Requires: docker",
		"Detected binary file",
		"File moved to /tmp/catch",
		"Removed old file: /tmp/catch",
		"copying catch",
		"adding unit catch.service",
		"Installing service catch",
		"File received",
		`Service "catch" installed`,
		"no env artifact found",
	}
	for _, msg := range ignored {
		if !isIgnoredInstallProgressLine(msg) {
			t.Fatalf("isIgnoredInstallProgressLine(%q) = false, want true", msg)
		}
	}

	important := []string{"Warning: docker missing", "Error: install failed", "operation failed", "runtime error", "tailscale OAuth setup failed: tag rejected"}
	for _, msg := range important {
		if !isImportantInitLine(msg) {
			t.Fatalf("isImportantInitLine(%q) = false, want true", msg)
		}
	}
	if isImportantInitLine("all good") {
		t.Fatal("isImportantInitLine all good = true, want false")
	}
}

func TestRedactSensitiveInitLine(t *testing.T) {
	got := redactSensitiveInitLine("key tskey-auth-secret, next tskey-api-secret\"")
	if strings.Contains(got, "secret") {
		t.Fatalf("redactSensitiveInitLine leaked secret: %q", got)
	}
	want := "key [tailscale-key-redacted], next [tailscale-key-redacted]\""
	if got != want {
		t.Fatalf("redactSensitiveInitLine = %q, want %q", got, want)
	}
}

func TestShouldFlushPartial(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "   "},
		{input: "Password:", want: true},
		{input: "Continue [y/n]", want: true},
		{input: "Name:", want: true},
		{input: "ordinary output"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := shouldFlushPartial(tt.input); got != tt.want {
				t.Fatalf("shouldFlushPartial(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
