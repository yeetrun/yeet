// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseCRAPReportKeys(t *testing.T) {
	report := `
FAIL: 2 function(s) exceed max CRAP score 30
exit status 1
       CRAP     Complexity   Coverage   Function                         Location
FAIL   2550.0   50           0.0%       (*FileInstaller).installOnClose  pkg/catch/installer_file.go:533
FAIL   361.0    61           56.8%      HandleSvcCmd                     pkg/yeet/svc_cmd.go:89
`

	got, err := parseCRAPReport([]byte(report))
	if err != nil {
		t.Fatalf("parseCRAPReport returned error: %v", err)
	}

	want := []hotspot{
		{Key: "crap|pkg/catch/installer_file.go|(*FileInstaller).installOnClose", Detail: "pkg/catch/installer_file.go (*FileInstaller).installOnClose"},
		{Key: "crap|pkg/yeet/svc_cmd.go|HandleSvcCmd", Detail: "pkg/yeet/svc_cmd.go HandleSvcCmd"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCRAPReport mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseGolangCIJSONKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg/catchrpc"), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	source := []byte(`package catchrpc

func Call() {
	defer resp.Body.Close()
}
`)
	if err := os.WriteFile(filepath.Join(dir, "pkg/catchrpc/client.go"), source, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Chdir(dir)

	report := `{
  "Issues": [
    {
      "FromLinter": "cyclop",
      "Text": "calculated cyclomatic complexity for function main is 18, max is 10",
      "Pos": {"Filename": "cmd/catch/catch.go", "Line": 103}
    },
    {
      "FromLinter": "gocognit",
      "Text": "cognitive complexity 94 of func ` + "`HandleSvcCmd`" + ` is high (> 30)",
      "Pos": {"Filename": "pkg/yeet/svc_cmd.go", "Line": 89}
    },
    {
      "FromLinter": "errcheck",
      "Text": "Error return value of ` + "`resp.Body.Close`" + ` is not checked",
      "Pos": {"Filename": "pkg/catchrpc/client.go", "Line": 4},
      "SourceLines": ["\tdefer resp.Body.Close()"]
    }
  ]
}`

	got, err := parseGolangCIJSON([]byte(report))
	if err != nil {
		t.Fatalf("parseGolangCIJSON returned error: %v", err)
	}

	want := []hotspot{
		{Key: "cyclop|cmd/catch/catch.go|main", Detail: "cmd/catch/catch.go main (cyclop)"},
		{Key: "errcheck|pkg/catchrpc/client.go|Call|source:defer resp.Body.Close()|Error return value of `resp.Body.Close` is not checked", Detail: "pkg/catchrpc/client.go:4 Error return value of `resp.Body.Close` is not checked (errcheck)"},
		{Key: "gocognit|pkg/yeet/svc_cmd.go|HandleSvcCmd", Detail: "pkg/yeet/svc_cmd.go HandleSvcCmd (gocognit)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGolangCIJSON mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFunctionAtLineIncludesReceiver(t *testing.T) {
	source := []byte(`package demo

type Server struct{}

func (s *Server) Run() error {
	return nil
}
`)
	got := functionAtLine(source, 5)
	if got != "(*Server).Run" {
		t.Fatalf("functionAtLine = %q, want %q", got, "(*Server).Run")
	}
}

func TestCompareHotspotsAllowsBurnDownButRejectsNew(t *testing.T) {
	baseline := map[string]bool{
		"crap|pkg/old.go|oldRisk":   true,
		"crap|pkg/gone.go|goneRisk": true,
	}
	current := []hotspot{
		{Key: "crap|pkg/old.go|oldRisk", Detail: "pkg/old.go oldRisk"},
		{Key: "crap|pkg/new.go|newRisk", Detail: "pkg/new.go newRisk"},
	}

	gotNew, gotResolved := compareHotspots(baseline, current)

	if len(gotNew) != 1 || gotNew[0].Key != "crap|pkg/new.go|newRisk" {
		t.Fatalf("new hotspots mismatch: %#v", gotNew)
	}
	if !reflect.DeepEqual(gotResolved, []string{"crap|pkg/gone.go|goneRisk"}) {
		t.Fatalf("resolved hotspots mismatch: %#v", gotResolved)
	}
}

func TestRunCLIWritesBaselineAndRejectsNewHotspot(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "crap.txt")
	baselinePath := filepath.Join(dir, "baseline.txt")

	report := []byte(`
       CRAP     Complexity   Coverage   Function       Location
FAIL   42.0     6            0.0%       riskyFunction  pkg/risk.go:10
`)
	if err := os.WriteFile(reportPath, report, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{
		"-kind", "crap",
		"-report", reportPath,
		"-baseline", baselinePath,
		"-write-baseline",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("write baseline code=%d stderr=%s", code, stderr.String())
	}

	code = runCLI([]string{
		"-kind", "crap",
		"-report", reportPath,
		"-baseline", baselinePath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("baseline check code=%d stderr=%s", code, stderr.String())
	}

	newReportPath := filepath.Join(dir, "new-crap.txt")
	newReport := []byte(`
       CRAP     Complexity   Coverage   Function       Location
FAIL   42.0     6            0.0%       riskyFunction  pkg/risk.go:10
FAIL   90.0     9            0.0%       newRisk        pkg/new.go:20
`)
	if err := os.WriteFile(newReportPath, newReport, 0o644); err != nil {
		t.Fatalf("write new report: %v", err)
	}

	code = runCLI([]string{
		"-kind", "crap",
		"-report", newReportPath,
		"-baseline", baselinePath,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("new hotspot check code=%d, want 1", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("pkg/new.go newRisk")) {
		t.Fatalf("stderr does not mention new hotspot: %s", stderr.String())
	}
}
