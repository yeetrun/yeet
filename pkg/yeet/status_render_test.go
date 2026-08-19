// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderStatusTablesSortedWithHostColumn(t *testing.T) {
	results := []hostStatusData{
		{
			Host: "host-b",
			Services: []statusService{
				{ServiceName: "svc-b", ServiceType: "docker", Components: []statusComponent{{Name: "b", Status: "running"}}},
				{ServiceName: "svc-a", ServiceType: "binary", Components: []statusComponent{{Name: "svc-a", Status: "stopped"}}},
			},
		},
		{
			Host: "host-a",
			Services: []statusService{
				{ServiceName: "svc-a", ServiceType: "docker", Components: []statusComponent{{Name: "a2", Status: "running"}, {Name: "a1", Status: "running"}}},
			},
		},
	}

	var buf bytes.Buffer
	if err := renderStatusTables(&buf, results, false); err != nil {
		t.Fatalf("renderStatusTables error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected output lines, got %q", buf.String())
	}
	if !strings.HasPrefix(lines[0], "SERVICE") {
		t.Fatalf("unexpected header: %q", lines[0])
	}

	got := strings.Join(lines[1:], "\n")
	got = strings.Join(strings.Fields(got), "\t")
	wantOrder := []string{
		"svc-a\thost-a\tdocker\ta1\trunning",
		"svc-a\thost-a\tdocker\ta2\trunning",
		"svc-a\thost-b\tbinary\t-\tstopped",
		"svc-b\thost-b\tdocker\tb\trunning",
	}
	for i, want := range wantOrder {
		if !strings.Contains(got, want) {
			t.Fatalf("missing row %d: %q\noutput:\n%s", i, want, buf.String())
		}
	}

	for i := 1; i < len(lines); i++ {
		normalized := strings.Join(strings.Fields(lines[i]), "\t")
		if i-1 < len(wantOrder) && !strings.HasPrefix(normalized, wantOrder[i-1]) {
			t.Fatalf("row %d = %q, want prefix %q", i, lines[i], wantOrder[i-1])
		}
	}
}

func TestRenderStatusTablesAggregatesDockerServices(t *testing.T) {
	results := []hostStatusData{
		{
			Host: "host-a",
			Services: []statusService{
				{ServiceName: "svc-a", ServiceType: "docker", Components: []statusComponent{
					{Name: "a1", Status: "running"},
					{Name: "a2", Status: "running"},
				}},
				{ServiceName: "svc-b", ServiceType: "docker", Components: []statusComponent{
					{Name: "b1", Status: "stopped"},
					{Name: "b2", Status: "stopped"},
				}},
				{ServiceName: "svc-c", ServiceType: "docker", Components: []statusComponent{
					{Name: "c1", Status: "running"},
					{Name: "c2", Status: "stopped"},
				}},
			},
		},
	}

	var buf bytes.Buffer
	if err := renderStatusTables(&buf, results, true); err != nil {
		t.Fatalf("renderStatusTables error: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected output lines, got %q", output)
	}
	if !strings.Contains(lines[0], "CONTAINERS") {
		t.Fatalf("expected CONTAINERS header, got %q", lines[0])
	}
	if !strings.Contains(output, "running (2)") {
		t.Fatalf("expected running summary, got %q", output)
	}
	if !strings.Contains(output, "stopped (2)") {
		t.Fatalf("expected stopped summary, got %q", output)
	}
	if !strings.Contains(output, "partial (1/2)") {
		t.Fatalf("expected partial summary, got %q", output)
	}
}

func TestRenderStatusTablesTruncatesContainers(t *testing.T) {
	results := []hostStatusData{
		{
			Host: "host-a",
			Services: []statusService{
				{ServiceName: "svc-a", ServiceType: "docker", Components: []statusComponent{
					{Name: "alpha"},
					{Name: "bravo"},
					{Name: "charlie"},
					{Name: "delta"},
					{Name: "echo"},
					{Name: "foxtrot"},
					{Name: "golf"},
				}},
			},
		},
	}

	var buf bytes.Buffer
	if err := renderStatusTables(&buf, results, true); err != nil {
		t.Fatalf("renderStatusTables error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "...") {
		t.Fatalf("expected truncated containers list, got %q", output)
	}
}

func TestRenderStatusTablesStylesTTYHeadingsOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	oldIsTerminal := isTerminalFn
	t.Cleanup(func() { isTerminalFn = oldIsTerminal })
	isTerminalFn = func(fd int) bool { return fd == 42 }

	out := &fdBuffer{fd: 42}
	results := []hostStatusData{{
		Host: "host-a",
		Services: []statusService{{
			ServiceName: "svc-a",
			ServiceType: "binary",
			Components:  []statusComponent{{Name: "svc-a", Status: "running"}},
		}},
	}}
	if err := renderStatusTables(out, results, false); err != nil {
		t.Fatalf("renderStatusTables error: %v", err)
	}

	text := out.String()
	for _, heading := range []string{"SERVICE", "HOST", "TYPE", "CONTAINER", "STATUS"} {
		styled := "\x1b[1;36m" + heading + "\x1b[m"
		if !strings.Contains(text, styled) {
			t.Fatalf("status output missing styled heading %q:\n%s", heading, text)
		}
		text = strings.ReplaceAll(text, styled, heading)
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("status output styled more than its headings:\n%s", out.String())
	}
	if !strings.Contains(text, "svc-a") || !strings.Contains(text, "host-a") || !strings.Contains(text, "running") {
		t.Fatalf("status row changed:\n%s", out.String())
	}
}
