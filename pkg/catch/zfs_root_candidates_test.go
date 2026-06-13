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

func TestZFSServiceRootCandidatesRanksVMRootFromClonedZVolChildren(t *testing.T) {
	out := strings.Join([]string{
		"pool\tfilesystem\t/pool\t1000\t400\t100\t-\ton\toff\tyes",
		"pool/workloads\tfilesystem\t/pool/workloads\t1000\t300\t1\t-\ton\toff\tyes",
		"pool/workloads/app-a\tfilesystem\t/pool/workloads/app-a\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/workloads/app-b\tfilesystem\t/pool/workloads/app-b\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/workloads/app-c\tfilesystem\t/pool/workloads/app-c\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/machines\tfilesystem\t/pool/machines\t1000\t30\t1\t-\ton\toff\tyes",
		"pool/machines/devbox\tfilesystem\t/pool/machines/devbox\t1000\t10\t1\t-\ton\toff\tyes",
		"pool/machines/devbox/disk0\tvolume\t-\t1000\t10\t10\tpool/templates/ubuntu/disk0@snap\t-\toff\t-",
		"pool/machines/buildbox\tfilesystem\t/pool/machines/buildbox\t1000\t10\t1\t-\ton\toff\tyes",
		"pool/machines/buildbox/disk0\tvolume\t-\t1000\t10\t10\tpool/templates/ubuntu/disk0@snap\t-\toff\t-",
		"pool/templates\tfilesystem\t/pool/templates\t1000\t20\t1\t-\ton\toff\tyes",
		"pool/templates/ubuntu\tfilesystem\t/pool/templates/ubuntu\t1000\t20\t1\t-\ton\toff\tyes",
		"pool/templates/ubuntu/disk0\tvolume\t-\t1000\t20\t20\t-\t-\toff\t-",
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
	if resp.Candidates[0].Dataset != "pool/machines" {
		t.Fatalf("top candidate = %#v, want pool/machines", resp.Candidates[0])
	}
	if resp.Candidates[0].VMChildCount != 2 {
		t.Fatalf("vm child count = %d, want 2", resp.Candidates[0].VMChildCount)
	}
	if resp.Candidates[0].SuggestedDataset != "pool/machines/devbox" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
	for _, candidate := range resp.Candidates {
		if strings.HasPrefix(candidate.Dataset, "pool/templates") {
			t.Fatalf("image template dataset returned: %#v", candidate)
		}
	}
}

func TestZFSServiceRootCandidatesRanksServiceRootForComposeFromPlainChildren(t *testing.T) {
	out := strings.Join([]string{
		"pool\tfilesystem\t/pool\t1000\t400\t100\t-\ton\toff\tyes",
		"pool/workloads\tfilesystem\t/pool/workloads\t1000\t300\t1\t-\ton\toff\tyes",
		"pool/workloads/radarr\tfilesystem\t/pool/workloads/radarr\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/workloads/sonarr\tfilesystem\t/pool/workloads/sonarr\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/machines\tfilesystem\t/pool/machines\t1000\t30\t1\t-\ton\toff\tyes",
		"pool/machines/devbox\tfilesystem\t/pool/machines/devbox\t1000\t10\t1\t-\ton\toff\tyes",
		"pool/machines/devbox/disk0\tvolume\t-\t1000\t10\t10\tpool/templates/ubuntu/disk0@snap\t-\toff\t-",
		"pool/machines/buildbox\tfilesystem\t/pool/machines/buildbox\t1000\t10\t1\t-\ton\toff\tyes",
		"pool/machines/buildbox/disk0\tvolume\t-\t1000\t10\t10\tpool/templates/ubuntu/disk0@snap\t-\toff\t-",
		"pool/machines/testbox\tfilesystem\t/pool/machines/testbox\t1000\t10\t1\t-\ton\toff\tyes",
		"pool/machines/testbox/disk0\tvolume\t-\t1000\t10\t10\tpool/templates/ubuntu/disk0@snap\t-\toff\t-",
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
	if resp.Candidates[0].Dataset != "pool/workloads" {
		t.Fatalf("top candidate = %#v, want pool/workloads", resp.Candidates[0])
	}
	if resp.Candidates[0].ServiceChildCount != 2 {
		t.Fatalf("service child count = %d, want 2", resp.Candidates[0].ServiceChildCount)
	}
	if resp.Candidates[0].SuggestedDataset != "pool/workloads/radarr" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
	for _, candidate := range resp.Candidates {
		if candidate.Dataset == "pool/workloads/radarr" || candidate.Dataset == "pool/workloads/sonarr" {
			t.Fatalf("service leaf dataset returned: %#v", candidate)
		}
	}
}

func TestZFSServiceRootCandidatesRanksMoreSpecificServiceRootAboveBusyPoolRoot(t *testing.T) {
	out := strings.Join([]string{
		"tank\tfilesystem\t/tank\t1000\t400\t100\t-\ton\toff\tyes",
		"tank/media\tfilesystem\t/tank/media\t1000\t10\t10\t-\ton\toff\tyes",
		"tank/backups\tfilesystem\t/tank/backups\t1000\t10\t10\t-\ton\toff\tyes",
		"tank/workloads\tfilesystem\t/tank/workloads\t1000\t300\t1\t-\ton\toff\tyes",
		"tank/workloads/radarr\tfilesystem\t/tank/workloads/radarr\t1000\t10\t10\t-\ton\toff\tyes",
		"tank/workloads/sonarr\tfilesystem\t/tank/workloads/sonarr\t1000\t10\t10\t-\ton\toff\tyes",
		"tank/workloads/prowlarr\tfilesystem\t/tank/workloads/prowlarr\t1000\t10\t10\t-\ton\toff\tyes",
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
	if resp.Candidates[0].Dataset != "tank/workloads" {
		t.Fatalf("top candidate = %#v, want tank/workloads before generic pool root", resp.Candidates[0])
	}
	if resp.Candidates[0].SuggestedDataset != "tank/workloads/radarr" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
}

func TestZFSServiceRootCandidatesRanksImageRootsBelowServiceRoots(t *testing.T) {
	out := strings.Join([]string{
		"pool\tfilesystem\t/pool\t1000\t400\t100\t-\ton\toff\tyes",
		"pool/workloads\tfilesystem\t/pool/workloads\t1000\t300\t1\t-\ton\toff\tyes",
		"pool/workloads/radarr\tfilesystem\t/pool/workloads/radarr\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/workloads/sonarr\tfilesystem\t/pool/workloads/sonarr\t1000\t10\t10\t-\ton\toff\tyes",
		"pool/templates\tfilesystem\t/pool/templates\t1000\t20\t1\t-\ton\toff\tyes",
		"pool/templates/ubuntu\tfilesystem\t/pool/templates/ubuntu\t1000\t20\t1\t-\ton\toff\tyes",
		"pool/templates/ubuntu/disk0\tvolume\t-\t1000\t20\t20\t-\t-\toff\t-",
		"pool/templates/nixos\tfilesystem\t/pool/templates/nixos\t1000\t20\t1\t-\ton\toff\tyes",
		"pool/templates/nixos/disk0\tvolume\t-\t1000\t20\t20\t-\t-\toff\t-",
		"pool/templates/debian\tfilesystem\t/pool/templates/debian\t1000\t20\t1\t-\ton\toff\tyes",
		"pool/templates/debian/disk0\tvolume\t-\t1000\t20\t20\t-\t-\toff\t-",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "compose",
		Service:  "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if len(resp.Candidates) == 0 {
		t.Fatal("no candidates returned")
	}
	if resp.Candidates[0].Dataset != "pool/workloads" {
		t.Fatalf("top candidate = %#v, want pool/workloads", resp.Candidates[0])
	}
	for _, candidate := range resp.Candidates {
		if candidate.Dataset == "pool/templates/ubuntu" || candidate.Dataset == "pool/templates/nixos" || candidate.Dataset == "pool/templates/debian" {
			t.Fatalf("image template leaf dataset returned: %#v", candidate)
		}
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
