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
	const service = "long-service-name-for-alignment"
	results := []hostStatusData{{
		Host: "host-a",
		Services: []statusService{{
			ServiceName: service,
			ServiceType: "binary",
			Components:  []statusComponent{{Name: service, Status: "running"}},
		}},
	}}
	if err := renderStatusTables(out, results, false); err != nil {
		t.Fatalf("renderStatusTables error: %v", err)
	}

	rendered := out.String()
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("status output lines = %d, want 2:\n%s", len(lines), rendered)
	}
	if !strings.HasPrefix(lines[0], "\x1b[1;36m") || !strings.HasSuffix(lines[0], "\x1b[m") {
		t.Fatalf("status header row is not styled as a heading:\n%s", rendered)
	}
	if strings.Contains(lines[1], "\x1b[") {
		t.Fatalf("status data row is styled:\n%s", rendered)
	}

	text := stripHeadingANSI(rendered)
	for _, heading := range []string{"SERVICE", "HOST", "TYPE", "CONTAINER", "STATUS"} {
		if !strings.Contains(text, heading) {
			t.Fatalf("status output missing styled heading %q:\n%s", heading, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("status output styled more than its headings:\n%s", out.String())
	}
	if !strings.Contains(text, service) || !strings.Contains(text, "host-a") || !strings.Contains(text, "running") {
		t.Fatalf("status row changed:\n%s", out.String())
	}

	plainLines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	headings := []string{"SERVICE", "HOST", "TYPE", "CONTAINER", "STATUS"}
	values := []string{service, "host-a", "binary", "-", "running"}
	headingCursor := 0
	valueCursor := 0
	for i := range headings {
		headingColumn := headingCursor + strings.Index(plainLines[0][headingCursor:], headings[i])
		valueColumn := valueCursor + strings.Index(plainLines[1][valueCursor:], values[i])
		if headingColumn != valueColumn {
			t.Errorf("%s column starts at %d, row value %q starts at %d:\n%s", headings[i], headingColumn, values[i], valueColumn, text)
		}
		headingCursor = headingColumn + len(headings[i])
		valueCursor = valueColumn + len(values[i])
	}
}
