// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestZFSServiceRootCandidatesRanksVMRoot(t *testing.T) {
	out := strings.Join([]string{
		"flash\tfilesystem\t/flash\t1000\t400\t100\t-\ton\toff\tyes",
		"flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff\tyes",
		"flash/yeet/vms\tfilesystem\t/flash/yeet/vms\t1000\t30\t1\t-\ton\toff\tyes",
		"flash/yeet/vms/devbox\tfilesystem\t/flash/yeet/vms/devbox\t1000\t10\t1\t-\ton\toff\tyes",
		"flash/yeet/vms/devbox/root\tvolume\t-\t1000\t10\t10\tflash/yeet/vm-images/ubuntu/root@snap\t-\toff\t-",
		"flash/yeet/vm-images\tfilesystem\t/flash/yeet/vm-images\t1000\t20\t1\t-\ton\toff\tyes",
		"flash/yeet/vm-images/ubuntu\tfilesystem\t/flash/yeet/vm-images/ubuntu\t1000\t20\t1\t-\ton\toff\tyes",
		"flash/yeet/vm-images/ubuntu/root\tvolume\t-\t1000\t20\t20\t-\t-\toff\t-",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "vm",
		Service:  "devbox",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if resp.State != catchrpc.ZFSRootDiscoveryAvailable {
		t.Fatalf("state = %q, want available", resp.State)
	}
	if len(resp.Candidates) == 0 {
		t.Fatal("no candidates returned")
	}
	if resp.Candidates[0].Dataset != "flash/yeet/vms" {
		t.Fatalf("top candidate = %#v, want flash/yeet/vms", resp.Candidates[0])
	}
	if resp.Candidates[0].SuggestedDataset != "flash/yeet/vms/devbox" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
	for _, candidate := range resp.Candidates {
		if strings.Contains(candidate.Dataset, "/vm-images") {
			t.Fatalf("internal vm-images dataset returned: %#v", candidate)
		}
	}
}

func TestZFSServiceRootCandidatesRanksServiceRootForCompose(t *testing.T) {
	out := strings.Join([]string{
		"flash\tfilesystem\t/flash\t1000\t400\t100\t-\ton\toff\tyes",
		"flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff\tyes",
		"flash/yeet/radarr\tfilesystem\t/flash/yeet/radarr\t1000\t10\t10\t-\ton\toff\tyes",
		"flash/yeet/sonarr\tfilesystem\t/flash/yeet/sonarr\t1000\t10\t10\t-\ton\toff\tyes",
		"flash/yeet/vms\tfilesystem\t/flash/yeet/vms\t1000\t30\t1\t-\ton\toff\tyes",
		"flash/yeet/vms/devbox\tfilesystem\t/flash/yeet/vms/devbox\t1000\t10\t1\t-\ton\toff\tyes",
		"flash/yeet/vms/devbox/root\tvolume\t-\t1000\t10\t10\tflash/yeet/vm-images/ubuntu/root@snap\t-\toff\t-",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "compose",
		Service:  "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if len(resp.Candidates) < 2 {
		t.Fatalf("candidates = %#v, want at least two", resp.Candidates)
	}
	if resp.Candidates[0].Dataset != "flash/yeet" {
		t.Fatalf("top candidate = %#v, want flash/yeet", resp.Candidates[0])
	}
	if resp.Candidates[0].SuggestedDataset != "flash/yeet/radarr" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
}

func TestZFSServiceRootCandidatesRanksNamedServiceRootAboveBusyPoolRoot(t *testing.T) {
	out := strings.Join([]string{
		"tank\tfilesystem\t/tank\t1000\t400\t100\t-\ton\toff\tyes",
		"tank/media\tfilesystem\t/tank/media\t1000\t10\t10\t-\ton\toff\tyes",
		"tank/backups\tfilesystem\t/tank/backups\t1000\t10\t10\t-\ton\toff\tyes",
		"tank/yeet\tfilesystem\t/tank/yeet\t1000\t300\t1\t-\ton\toff\tyes",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "compose",
		Service:  "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if len(resp.Candidates) < 2 {
		t.Fatalf("candidates = %#v, want at least two", resp.Candidates)
	}
	if resp.Candidates[0].Dataset != "tank/yeet" {
		t.Fatalf("top candidate = %#v, want tank/yeet before generic pool root", resp.Candidates[0])
	}
	if resp.Candidates[0].SuggestedDataset != "tank/yeet/radarr" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
}

func TestZFSServiceRootCandidatesCapabilityStates(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		err    error
		want   catchrpc.ZFSRootDiscoveryState
	}{
		{name: "zfs missing", stderr: "zfs: command not found", err: errors.New("exec: zfs: executable file not found"), want: catchrpc.ZFSRootDiscoveryZFSMissing},
		{name: "no filesystems", stdout: "tank/root\tvolume\t-\t1\t1\t1\t-\t-\toff\t-\n", want: catchrpc.ZFSRootDiscoveryNoFilesystems},
		{name: "command error", stderr: "permission denied", err: errZFSCommandFailed, want: catchrpc.ZFSRootDiscoveryError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(tt.stdout, tt.stderr, tt.err), catchrpc.ZFSServiceRootCandidatesRequest{})
			if err != nil {
				t.Fatalf("zfsServiceRootCandidates returned error: %v", err)
			}
			if resp.State != tt.want {
				t.Fatalf("state = %q, want %q response=%#v", resp.State, tt.want, resp)
			}
		})
	}
}

func TestServerZFSServiceRootCandidatesUsesConfiguredRunner(t *testing.T) {
	out := "flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff\tyes\n"
	server := &Server{zfsRunner: fakeZFSListRunner(out, "", nil)}
	resp, err := server.zfsServiceRootCandidates(context.Background(), catchrpc.ZFSServiceRootCandidatesRequest{
		Service: "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if resp.State != catchrpc.ZFSRootDiscoveryAvailable {
		t.Fatalf("state = %q, want available", resp.State)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].SuggestedDataset != "flash/yeet/radarr" {
		t.Fatalf("candidates = %#v", resp.Candidates)
	}
}

func TestZFSServiceRootCandidatesSkipsNonNormalMountpoints(t *testing.T) {
	out := strings.Join([]string{
		"flash/none\tfilesystem\tnone\t1000\t300\t1\t-\ton\toff\tyes",
		"flash/relative\tfilesystem\tflash/relative\t1000\t300\t1\t-\ton\toff\tyes",
		"flash/legacy\tfilesystem\tlegacy\t1000\t300\t1\t-\ton\toff\tyes",
		"flash/normal\tfilesystem\t/flash/normal\t1000\t300\t1\t-\ton\toff\tyes",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Service: "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if resp.State != catchrpc.ZFSRootDiscoveryAvailable {
		t.Fatalf("state = %q, want available", resp.State)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want one normal mountpoint", resp.Candidates)
	}
	if resp.Candidates[0].Dataset != "flash/normal" {
		t.Fatalf("candidate = %#v, want flash/normal", resp.Candidates[0])
	}
}

func TestZFSServiceRootCandidatesSkipsUnmountedFilesystems(t *testing.T) {
	out := strings.Join([]string{
		"flash/unmounted\tfilesystem\t/flash/unmounted\t1000\t300\t1\t-\ton\toff\tno",
		"flash/normal\tfilesystem\t/flash/normal\t1000\t300\t1\t-\ton\toff\tyes",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Service: "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if resp.State != catchrpc.ZFSRootDiscoveryAvailable {
		t.Fatalf("state = %q, want available", resp.State)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want one mounted filesystem", resp.Candidates)
	}
	if resp.Candidates[0].Dataset != "flash/normal" {
		t.Fatalf("candidate = %#v, want flash/normal", resp.Candidates[0])
	}
}

func TestSuggestedZFSDatasetUsesTrailingSlashWithoutService(t *testing.T) {
	if got := suggestedZFSDataset("flash/yeet/vms", ""); got != "flash/yeet/vms/" {
		t.Fatalf("suggested empty service = %q", got)
	}
	if got := suggestedZFSDataset("flash/yeet/vms/", " devbox "); got != "flash/yeet/vms/devbox" {
		t.Fatalf("suggested service = %q", got)
	}
}

func TestParseZFSRootCandidateRowsRejectsMalformedRows(t *testing.T) {
	_, err := parseZFSRootCandidateRows("flash\tfilesystem\n")
	if err == nil || !strings.Contains(err.Error(), "invalid zfs list row") {
		t.Fatalf("parse error = %v, want invalid row", err)
	}
}

func fakeZFSListRunner(stdout, stderr string, err error) zfsCommandRunner {
	return func(ctx context.Context, args ...string) (string, string, error) {
		want := []string{"list", "-H", "-p", "-o", "name,type,mountpoint,available,used,refer,origin,canmount,readonly,mounted", "-t", "filesystem,volume"}
		if !reflect.DeepEqual(args, want) {
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
		return stdout, stderr, err
	}
}
