// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/copyutil"
	cdb "github.com/yeetrun/yeet/pkg/db"
)

type ttyInstallErrWriter struct {
	err error
}

func (w ttyInstallErrWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestParseCopyExecArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    copyExecArgs
		wantErr string
	}{
		{
			name: "to archive compress",
			args: []string{"--to", "data/app", "-a", "-z"},
			want: copyExecArgs{To: "data/app", Recursive: true, Archive: true, Compress: true},
		},
		{
			name: "from recursive",
			args: []string{"--from", "logs", "--recursive"},
			want: copyExecArgs{From: "logs", Recursive: true},
		},
		{
			name:    "from missing value",
			args:    []string{"--from"},
			wantErr: "copy --from requires a value",
		},
		{
			name:    "to missing value",
			args:    []string{"--to"},
			wantErr: "copy --to requires a value",
		},
		{
			name:    "both directions",
			args:    []string{"--from", "a", "--to", "b"},
			wantErr: "copy requires either --from or --to",
		},
		{
			name:    "missing direction",
			args:    []string{"-r"},
			wantErr: "copy requires --from or --to",
		},
		{
			name:    "invalid arg",
			args:    []string{"--bad"},
			wantErr: `invalid copy argument "--bad"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCopyExecArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseCopyExecArgs error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCopyExecArgs returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseCopyExecArgs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNormalizeCopyRelPath(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		allowEmpty bool
		want       string
		wantErr    string
	}{
		{name: "strips data prefix", raw: " data/foo ", want: "foo"},
		{name: "cleans dot slash and parent", raw: "./data/foo/../bar", want: "bar"},
		{name: "allows dot when empty allowed", raw: ".", allowEmpty: true, want: ""},
		{name: "rejects dot when empty denied", raw: ".", wantErr: "copy path must not be empty"},
		{name: "rejects absolute", raw: "/tmp/file", wantErr: "copy path must be relative"},
		{name: "rejects parent traversal", raw: "../etc/passwd", wantErr: `invalid copy path "../etc/passwd"`},
		{name: "data root empty allowed", raw: "data", allowEmpty: true, want: ""},
		{name: "data root empty denied", raw: "data", wantErr: "copy path must not be empty"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCopyRelPath(tc.raw, tc.allowEmpty)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("normalizeCopyRelPath error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeCopyRelPath returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeCopyRelPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCopyToRemoteWritesFile(t *testing.T) {
	server := newTestServer(t)
	input := bytes.NewBufferString("hello")
	execer := &ttyExecer{
		s:  server,
		sn: "svc-copy",
		rw: input,
	}

	if err := execer.copyToRemote(copyExecArgs{To: "data/sub/file.txt"}); err != nil {
		t.Fatalf("copyToRemote: %v", err)
	}

	dst := filepath.Join(server.serviceDataDir("svc-copy"), "sub", "file.txt")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("copied file = %q, want hello", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("copied file mode = %v, want 0644", got)
	}
}

func TestCopyToRemoteExtractsArchiveAtDataRoot(t *testing.T) {
	server := newTestServer(t)
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "file.txt"), []byte("archived"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	var input bytes.Buffer
	if err := copyutil.TarDirectory(&input, src, ""); err != nil {
		t.Fatalf("tar source: %v", err)
	}
	execer := &ttyExecer{
		s:  server,
		sn: "svc-archive",
		rw: &input,
	}

	if err := execer.copyToRemote(copyExecArgs{To: "data", Archive: true}); err != nil {
		t.Fatalf("copyToRemote archive: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(server.serviceDataDir("svc-archive"), "nested", "file.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != "archived" {
		t.Fatalf("extracted file = %q, want archived", got)
	}
}

func TestCopyFromRemoteWritesFileHeaderAndPayload(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("svc-copy", ""); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	src := filepath.Join(server.serviceDataDir("svc-copy"), "file.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "svc-copy",
		rw: &out,
	}

	if err := execer.copyFromRemote(copyExecArgs{From: "data/file.txt"}); err != nil {
		t.Fatalf("copyFromRemote: %v", err)
	}

	br := bufio.NewReader(&out)
	kind, base, err := copyutil.ReadHeader(br)
	if err != nil {
		t.Fatalf("read copy header: %v", err)
	}
	if kind != "file" || base != "file.txt" {
		t.Fatalf("copy header = (%q, %q), want (file, file.txt)", kind, base)
	}
	payload, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q, want payload", payload)
	}
}

func TestCopyFromRemoteRequiresRecursiveForDirectory(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("svc-copy", ""); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.Mkdir(filepath.Join(server.serviceDataDir("svc-copy"), "logs"), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "svc-copy",
		rw: &out,
	}

	err := execer.copyFromRemote(copyExecArgs{From: "data/logs"})
	if err == nil || !strings.Contains(err.Error(), "copy requires recursive mode for directories") {
		t.Fatalf("copyFromRemote error = %v, want recursive directory error", err)
	}
}

func TestStageShowReturnsWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	execer := &ttyExecer{
		s:  newTestServer(t),
		sn: "svc-show",
		rw: readWriter{Reader: bytes.NewReader(nil), Writer: ttyInstallErrWriter{err: writeErr}},
	}

	err := execer.stageCmdFunc("show", cli.StageFlags{}, nil)
	if err == nil {
		t.Fatal("expected stage show write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("stage show error = %v, want %v", err, writeErr)
	}
}

func TestClearStageNoChangesReturnsWriteError(t *testing.T) {
	server := newTestServer(t)
	_, _, err := server.cfg.DB.MutateService("svc-empty", func(_ *cdb.Data, s *cdb.Service) error {
		s.ServiceType = cdb.ServiceTypeSystemd
		s.Artifacts = cdb.ArtifactStore{
			cdb.ArtifactBinary: {Refs: map[cdb.ArtifactRef]string{"latest": "/tmp/latest.bin"}},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate service: %v", err)
	}
	writeErr := errors.New("write failed")
	execer := &ttyExecer{
		s:  server,
		sn: "svc-empty",
		rw: readWriter{Reader: bytes.NewReader(nil), Writer: ttyInstallErrWriter{err: writeErr}},
	}

	err = execer.clearStage()
	if err == nil {
		t.Fatal("expected clearStage write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("clearStage error = %v, want %v", err, writeErr)
	}
}

func TestHumanReadableBytes(t *testing.T) {
	tests := []struct {
		bytes float64
		want  string
	}{
		{bytes: 512, want: "512.00 B"},
		{bytes: 1024, want: "1024.00 B"},
		{bytes: 1536, want: "1.50 KB"},
		{bytes: 2 * 1024 * 1024, want: "2.00 MB"},
	}

	for _, tc := range tests {
		if got := humanReadableBytes(tc.bytes); got != tc.want {
			t.Fatalf("humanReadableBytes(%v) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestRunCmdFuncBuildsInstallConfigWithoutLiveInstaller(t *testing.T) {
	var gotAction string
	var gotPayload string
	var gotCfg FileInstallerCfg
	execer := &ttyExecer{
		s:              newTestServer(t),
		sn:             "svc-run",
		user:           "app",
		payloadName:    "payload.bin",
		rawRW:          bytes.NewBufferString("binary-payload"),
		rw:             &bytes.Buffer{},
		bypassPtyInput: true,
		installFunc: func(action string, in io.Reader, cfg FileInstallerCfg) error {
			gotAction = action
			payload, err := io.ReadAll(in)
			if err != nil {
				t.Fatalf("ReadAll payload: %v", err)
			}
			gotPayload = string(payload)
			gotCfg = cfg
			return nil
		},
	}

	flags := cli.RunFlags{
		Pull:          true,
		Net:           "ts",
		TsVer:         "1.2.3",
		TsExit:        "exit-node",
		TsTags:        []string{"tag:web"},
		TsAuthKey:     "tskey-test",
		MacvlanParent: "eth0",
		MacvlanMac:    "02:00:00:00:00:01",
		MacvlanVlan:   42,
		Publish:       []string{"8080:80"},
	}
	if err := execer.runCmdFunc(flags, []string{"--flag"}); err != nil {
		t.Fatalf("runCmdFunc returned error: %v", err)
	}

	if gotAction != "run" {
		t.Fatalf("action = %q, want run", gotAction)
	}
	if gotPayload != "binary-payload" {
		t.Fatalf("payload = %q, want binary-payload", gotPayload)
	}
	if gotCfg.ServiceName != "svc-run" || gotCfg.User != "app" || gotCfg.PayloadName != "payload.bin" {
		t.Fatalf("installer cfg identity = %#v", gotCfg)
	}
	if !gotCfg.Pull {
		t.Fatal("expected pull flag to be copied")
	}
	if !reflect.DeepEqual(gotCfg.Args, []string{"--flag"}) {
		t.Fatalf("args = %#v, want --flag", gotCfg.Args)
	}
	if gotCfg.Network.Interfaces != "ts" || gotCfg.Network.Tailscale.Version != "1.2.3" {
		t.Fatalf("network cfg = %#v, want tailscale settings", gotCfg.Network)
	}
	if gotCfg.Network.Macvlan.Parent != "eth0" || gotCfg.Network.Macvlan.VLAN != 42 {
		t.Fatalf("macvlan cfg = %#v, want parent eth0 vlan 42", gotCfg.Network.Macvlan)
	}
	if !reflect.DeepEqual(gotCfg.Publish, []string{"8080:80"}) {
		t.Fatalf("publish = %#v, want 8080:80", gotCfg.Publish)
	}
}

func TestRunCmdFuncRejectsSystemServiceBeforeInstall(t *testing.T) {
	called := false
	execer := &ttyExecer{
		sn: SystemService,
		installFunc: func(string, io.Reader, FileInstallerCfg) error {
			called = true
			return nil
		},
	}

	err := execer.runCmdFunc(cli.RunFlags{}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot run, reserved service name") {
		t.Fatalf("run error = %v, want reserved service name", err)
	}
	if called {
		t.Fatal("install seam was called for reserved system service")
	}
}

func TestCronCmdFuncConvertsCronAndInstallsTimer(t *testing.T) {
	var gotCfg FileInstallerCfg
	execer := &ttyExecer{
		s:     newTestServer(t),
		sn:    "svc-cron",
		rawRW: bytes.NewBufferString("payload"),
		rw:    &bytes.Buffer{},
		installFunc: func(action string, in io.Reader, cfg FileInstallerCfg) error {
			if action != "cron" {
				t.Fatalf("action = %q, want cron", action)
			}
			gotCfg = cfg
			return nil
		},
	}

	if err := execer.cronCmdFunc("* * * * *", []string{"--hello"}); err != nil {
		t.Fatalf("cronCmdFunc returned error: %v", err)
	}
	if gotCfg.Timer == nil {
		t.Fatal("expected timer config")
	}
	if gotCfg.Timer.OnCalendar != "*-*-* *:*:00" || !gotCfg.Timer.Persistent {
		t.Fatalf("timer = %#v, want minutely persistent timer", gotCfg.Timer)
	}
	if !reflect.DeepEqual(gotCfg.Args, []string{"--hello"}) {
		t.Fatalf("args = %#v, want --hello", gotCfg.Args)
	}
}

func TestCronCmdFuncRejectsInvalidCronBeforeInstall(t *testing.T) {
	called := false
	execer := &ttyExecer{
		installFunc: func(string, io.Reader, FileInstallerCfg) error {
			called = true
			return nil
		},
	}

	err := execer.cronCmdFunc("* * *", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid cron expression") {
		t.Fatalf("cron error = %v, want invalid cron", err)
	}
	if called {
		t.Fatal("install seam was called for invalid cron")
	}
}

func TestCopyInstallPayloadCopiesBytesWithoutProgressForEnvFile(t *testing.T) {
	server := newTestServer(t)
	inst, err := NewFileInstaller(server, FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "svc-env"}})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	defer func() {
		inst.Fail()
		_ = inst.Close()
	}()
	ui := newRunUI(io.Discard, false, true, "env", "svc-env")
	execer := &ttyExecer{ctx: context.Background(), rawCloser: io.NopCloser(bytes.NewReader(nil))}

	if err := execer.copyInstallPayload(strings.NewReader("KEY=value\n"), FileInstallerCfg{EnvFile: true}, ui, inst); err != nil {
		t.Fatalf("copyInstallPayload returned error: %v", err)
	}
	if got := inst.Received(); got != float64(len("KEY=value\n")) {
		t.Fatalf("received = %v, want payload length", got)
	}
}

func TestCopyInitialInstallByteRejectsEmptyPayload(t *testing.T) {
	server := newTestServer(t)
	inst, err := NewFileInstaller(server, FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "svc-empty-payload"}})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	defer func() {
		inst.Fail()
		_ = inst.Close()
	}()
	ui := newRunUI(io.Discard, false, true, "run", "svc-empty-payload")

	err = copyInitialInstallByte(inst, strings.NewReader(""), ui)
	if err == nil || !strings.Contains(err.Error(), "failed to read binary") {
		t.Fatalf("copyInitialInstallByte error = %v, want read binary error", err)
	}
	if !inst.failed {
		t.Fatal("installer should be marked failed")
	}
}

func TestWaitForInstallUploadStartStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	execer := &ttyExecer{ctx: ctx}
	ui := newRunUI(io.Discard, false, true, "run", "svc-cancel")

	if execer.waitForInstallUploadStart(ui, make(chan struct{}), make(chan struct{})) {
		t.Fatal("waitForInstallUploadStart returned true after context cancellation")
	}
}

func TestSessionCloserCallsExit(t *testing.T) {
	closer := &exitRecordingCloser{}
	if err := (sessionCloser{Closer: closer}).Close(); err != nil {
		t.Fatalf("sessionCloser returned error: %v", err)
	}
	if closer.code != 0 {
		t.Fatalf("exit code = %d, want 0", closer.code)
	}
}

func TestStageCmdFuncRejectsSystemService(t *testing.T) {
	err := (&ttyExecer{sn: SystemService}).stageCmdFunc("show", cli.StageFlags{}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot stage system service") {
		t.Fatalf("stage error = %v, want system service error", err)
	}
}

func TestStageCmdFuncRejectsInvalidSubcommand(t *testing.T) {
	execer := &ttyExecer{
		s:  newTestServer(t),
		sn: "svc-stage-invalid",
		rw: &bytes.Buffer{},
	}

	err := execer.stageCmdFunc("bogus", cli.StageFlags{}, nil)
	if err == nil || !strings.Contains(err.Error(), `invalid argument "bogus"`) {
		t.Fatalf("stage error = %v, want invalid argument", err)
	}
}

func TestComposePathFromArtifactsPrefersStagedComposeFile(t *testing.T) {
	got, err := composePathFromArtifacts(cdb.ArtifactStore{
		cdb.ArtifactDockerComposeFile: {
			Refs: map[cdb.ArtifactRef]string{
				"latest": "/tmp/latest.yml",
				"staged": "/tmp/staged.yml",
			},
		},
	})
	if err != nil {
		t.Fatalf("composePathFromArtifacts returned error: %v", err)
	}
	if got != "/tmp/staged.yml" {
		t.Fatalf("compose path = %q, want staged", got)
	}
}

func TestCopyPayloadCompressionRoundTrip(t *testing.T) {
	var compressed bytes.Buffer
	w, closer := copyPayloadWriter(&compressed, true)
	if _, err := io.WriteString(w, "payload"); err != nil {
		t.Fatalf("write compressed payload: %v", err)
	}
	if err := closePayload(closer); err != nil {
		t.Fatalf("close compressed payload: %v", err)
	}

	r, readCloser, err := copyPayloadReader(&compressed, true)
	if err != nil {
		t.Fatalf("copyPayloadReader returned error: %v", err)
	}
	defer func() {
		if err := closePayload(readCloser); err != nil {
			t.Fatalf("close reader: %v", err)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed payload: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("payload = %q, want payload", got)
	}
}

type exitRecordingCloser struct {
	code int
}

func (c *exitRecordingCloser) Close() error { return nil }
func (c *exitRecordingCloser) Exit(code int) {
	c.code = code
}
