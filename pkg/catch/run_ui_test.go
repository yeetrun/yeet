// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlainRunUIOutputsKeyValueLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")
	ui.Start()
	ui.StartStep(runStepUpload)
	ui.DoneStep("16.61 MB @ 24.70 MB/s")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d (%q)", len(lines), buf.String())
	}
	if want := `action=run service=svc-a status=running step="Upload payload"`; lines[0] != want {
		t.Fatalf("unexpected first line: %q", lines[0])
	}
	if want := `action=run service=svc-a status=ok step="Upload payload" detail="16.61 MB @ 24.70 MB/s"`; lines[1] != want {
		t.Fatalf("unexpected second line: %q", lines[1])
	}
}

func TestRunUIDockerPlainOutputRemainsStructured(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "api@yeet-pve1")

	ui.Start()
	ui.StartStep(runStepUpload)
	ui.DoneStep("701.00 B @ 1.90 KB/s")
	ui.StartStep(runStepDetect)
	ui.DoneStep("docker compose")
	ui.StartStep(runStepInstall)
	ui.DoneStep("")
	ui.Stop()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=api@yeet-pve1 status=running step="Upload payload"`,
		`action=run service=api@yeet-pve1 status=ok step="Upload payload" detail="701.00 B @ 1.90 KB/s"`,
		`action=run service=api@yeet-pve1 status=running step="Detect payload"`,
		`action=run service=api@yeet-pve1 status=ok step="Detect payload" detail="docker compose"`,
		`action=run service=api@yeet-pve1 status=running step="Install service"`,
		`action=run service=api@yeet-pve1 status=ok step="Install service"`,
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("plain docker output = %#v, want %#v", lines, want)
	}
}

func TestRunUIHandlesKnownProgressMessages(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.Printer("Detected binary file")
	ui.Printer("File received")
	ui.Printer("Installing service")
	ui.Printer(`Service "svc-a" installed`)
	ui.Printer("Service installed: svc-a")
	ui.Printer("Service restarted: svc-a")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=svc-a status=running step="Detect payload"`,
		`action=run service=svc-a status=ok step="Detect payload" detail=binary`,
		`action=run service=svc-a status=running step="Install service"`,
		`action=run service=svc-a status=ok step="Install service"`,
	}
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d (%q)", len(want), len(lines), buf.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestRunUIUpdateDetailPlainModeIsSilent(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.StartStep(runStepUpload)
	ui.UpdateDetail("16.61 MB")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected only the start-step line, got %d (%q)", len(lines), buf.String())
	}
	if want := `action=run service=svc-a status=running step="Upload payload"`; lines[0] != want {
		t.Fatalf("line = %q, want %q", lines[0], want)
	}
}

func TestDetectedFileDetail(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
		ok   bool
	}{
		{name: "detected file", msg: "Detected binary file", want: "binary", ok: true},
		{name: "wrong prefix", msg: "Found binary file"},
		{name: "wrong suffix", msg: "Detected binary payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := detectedFileDetail(tt.msg)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("detail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunUIDetailText(t *testing.T) {
	tests := []struct {
		name   string
		step   string
		detail string
		want   string
	}{
		{name: "without detail", step: runStepUpload, want: runStepUpload},
		{name: "with detail", step: runStepUpload, detail: "16.61 MB", want: "Upload payload 16.61 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runUIDetailText(tt.step, tt.detail); got != tt.want {
				t.Fatalf("runUIDetailText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunUITimedDetailText(t *testing.T) {
	tests := []struct {
		name    string
		step    string
		detail  string
		elapsed time.Duration
		want    string
	}{
		{name: "step only", step: "Wait for guest readiness", elapsed: 1200 * time.Millisecond, want: "Wait for guest readiness 1.2s"},
		{name: "step and detail", step: "Prepare disk", detail: "64 GB", elapsed: 2500 * time.Millisecond, want: "Prepare disk 64 GB 2.5s"},
		{name: "subsecond", step: "Start VM", elapsed: 80 * time.Millisecond, want: "Start VM 0.1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runUITimedDetailText(tt.step, tt.detail, tt.elapsed); got != tt.want {
				t.Fatalf("runUITimedDetailText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatRunUIElapsed(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 80 * time.Millisecond, want: "0.1s"},
		{elapsed: 1250 * time.Millisecond, want: "1.3s"},
		{elapsed: 12*time.Second + 340*time.Millisecond, want: "12.3s"},
		{elapsed: 70*time.Second + 100*time.Millisecond, want: "1m10s"},
	}
	for _, tt := range tests {
		if got := formatRunUIElapsed(tt.elapsed); got != tt.want {
			t.Fatalf("formatRunUIElapsed(%s) = %q, want %q", tt.elapsed, got, tt.want)
		}
	}
}

func TestShouldUpdateRunUISpinner(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		suspended  bool
		hasSpinner bool
		want       bool
	}{
		{name: "enabled active spinner", enabled: true, hasSpinner: true, want: true},
		{name: "plain mode", hasSpinner: true},
		{name: "suspended", enabled: true, suspended: true, hasSpinner: true},
		{name: "no spinner", enabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUpdateRunUISpinner(tt.enabled, tt.suspended, tt.hasSpinner)
			if got != tt.want {
				t.Fatalf("shouldUpdateRunUISpinner = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunUIQuietSuppressesOutput(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, true, "run", "svc-a")

	ui.Start()
	ui.StartStep(runStepUpload)
	ui.UpdateDetail("half")
	ui.DoneStep("done")
	ui.FailStep("failed")
	ui.Printer("hello")
	ui.Suspend()
	ui.Stop()

	if got := buf.String(); got != "" {
		t.Fatalf("quiet UI output = %q, want empty", got)
	}
}

func TestRunUIPrintBlockTTYWritesHumanLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, true, false, "run", "devbox@yeet-pve1")

	ui.PrintBlock([]string{
		"",
		"devbox@yeet-pve1",
		"SSH      yeet ssh devbox",
	})

	got := buf.String()
	for _, want := range []string{
		"\ndevbox@yeet-pve1\n",
		"SSH      yeet ssh devbox\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PrintBlock output missing %q in %q", want, got)
		}
	}
}

func TestRunUIPrintBlockPlainEmitsInfoLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "devbox@yeet-pve1")

	ui.PrintBlock([]string{
		"",
		"devbox@yeet-pve1",
		"SSH      yeet ssh devbox",
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=devbox@yeet-pve1 status=info detail=devbox@yeet-pve1`,
		`action=run service=devbox@yeet-pve1 status=info detail="SSH      yeet ssh devbox"`,
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("PrintBlock plain lines = %#v, want %#v", lines, want)
	}
}

func TestRunUIPrintBlockQuietSuppressesOutput(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, true, "run", "devbox@yeet-pve1")

	ui.PrintBlock([]string{"devbox@yeet-pve1"})

	if got := buf.String(); got != "" {
		t.Fatalf("PrintBlock quiet output = %q, want empty", got)
	}
}

func TestRunUIStartTimedStepTTYWritesElapsedUpdate(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, true, false, "run", "devbox@yeet-pve1")

	ui.StartTimedStep("Wait for guest readiness")
	ui.UpdateDetail("ssh")
	time.Sleep(150 * time.Millisecond)
	ui.DoneStep("ready")

	got := buf.String()
	for _, want := range []string{
		"Wait for guest readiness ssh",
		"✔ Wait for guest readiness",
		"ready",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("timed step output missing %q in %q", want, got)
		}
	}
}

func TestRunUIStopTimedStepKeepsDetailUntilUpdaterStops(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, true, false, "run", "devbox@yeet-pve1")
	stop := make(chan struct{})
	done := make(chan struct{})
	started := time.Now()

	ui.mu.Lock()
	ui.stepStartedAt = started
	ui.stepDetail = "ssh"
	ui.timedStop = stop
	ui.timedDone = done
	ui.mu.Unlock()

	stopped := make(chan struct{})
	go func() {
		ui.stopTimedStep()
		close(stopped)
	}()

	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("timed step stop was not signaled")
	}

	ui.mu.Lock()
	gotStarted := ui.stepStartedAt
	gotDetail := ui.stepDetail
	ui.mu.Unlock()
	if !gotStarted.Equal(started) || gotDetail != "ssh" {
		t.Fatalf("timed state before updater done = started:%v detail:%q, want preserved", gotStarted, gotDetail)
	}

	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("timed step stop did not finish")
	}

	ui.mu.Lock()
	gotStarted = ui.stepStartedAt
	gotDetail = ui.stepDetail
	ui.mu.Unlock()
	if !gotStarted.IsZero() || gotDetail != "" {
		t.Fatalf("timed state after updater done = started:%v detail:%q, want cleared", gotStarted, gotDetail)
	}
}

func TestRunUIStartStepStopsPreviousTimedStep(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, true, false, "run", "devbox@yeet-pve1")

	ui.StartTimedStep("Wait for guest readiness")
	ui.StartStep(runStepUpload)

	ui.mu.Lock()
	timedStop := ui.timedStop
	timedDone := ui.timedDone
	started := ui.stepStartedAt
	ui.mu.Unlock()
	if timedStop != nil || timedDone != nil || !started.IsZero() {
		t.Fatalf("timed state after plain StartStep = stop:%v done:%v started:%v, want cleared", timedStop, timedDone, started)
	}

	time.Sleep(150 * time.Millisecond)
	ui.DoneStep("done")

	if got := buf.String(); strings.Contains(got, runStepUpload+" 0.") {
		t.Fatalf("plain step received stale timed update: %q", got)
	}
}

func TestRunUIStopIsIdempotentAndSuspendSkipsNewSteps(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.StartStep(runStepUpload)
	ui.Suspend()
	ui.StartStep(runStepInstall)
	ui.Stop()
	ui.Stop()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d %q, want start line only", len(lines), buf.String())
	}
	if lines[0] != `action=run service=svc-a status=running step="Upload payload"` {
		t.Fatalf("start line = %q", lines[0])
	}
}

func TestRunUIPlainFailStepAndInfo(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.Printer("custom message")
	ui.StartStep(runStepInstall)
	ui.FailStep("boom")
	ui.FailStep("ignored")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=svc-a status=info detail="custom message"`,
		`action=run service=svc-a status=running step="Install service"`,
		`action=run service=svc-a status=err step="Install service" detail=boom`,
	}
	if len(lines) != len(want) {
		t.Fatalf("line count = %d, want %d (%q)", len(lines), len(want), buf.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestRunUIHeaderAndStatusFormatting(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.printHeader()
	ui.printStatus("OK", runStepUpload, "done")
	ui.printStatus("ERR", runStepInstall, "")
	ui.printStatus("WAIT", runStepDetect, "")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		"[+] yeet run svc-a",
		"✔ Upload payload (done)",
		"✖ Install service",
		"WAIT Detect payload",
	}
	if len(lines) != len(want) {
		t.Fatalf("line count = %d, want %d (%q)", len(lines), len(want), buf.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestPlainRunUIMarkHeaderDoneAndDoneWithoutCurrentAreNoops(t *testing.T) {
	var buf bytes.Buffer
	plain := newPlainRunUI(&buf, "run", "svc-a")

	plain.MarkHeaderDone()
	plain.DoneStep("ignored")
	plain.FailStep("ignored")

	if got := buf.String(); got != "" {
		t.Fatalf("plain no-op output = %q, want empty", got)
	}
	if !plain.headerDone {
		t.Fatal("MarkHeaderDone did not mark header done")
	}
}
