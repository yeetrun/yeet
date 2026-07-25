// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"tailscale.com/ipn/ipnstate"
)

func TestVerifyTailscaleSidecarRejectsTransitionalMainPID(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	const catchPID = 101
	const tailscaledPID = 202
	tailscaledPath := svc.artifactInstaller()[db.ArtifactTSBinary].dstPath
	catchPath := filepath.Join(filepath.Dir(tailscaledPath), "catch")
	var pidCalls, statusCalls, waits int
	var verifiedPIDs []int

	err := svc.verifyTailscaleSidecar(context.Background(), tailscaleLifecycleDeps{
		mainPID: func(unit string) (int, error) {
			if unit != "yeet-demo-ts.service" {
				t.Fatalf("unit = %q, want yeet-demo-ts.service", unit)
			}
			pidCalls++
			if pidCalls == 1 {
				return catchPID, nil
			}
			return tailscaledPID, nil
		},
		stat: func(path string) (os.FileInfo, error) {
			switch path {
			case tailscaledPath, fmt.Sprintf("/proc/%d/exe", tailscaledPID):
				return os.Stat(tailscaledPath)
			case fmt.Sprintf("/proc/%d/exe", catchPID):
				return os.Stat(catchPath)
			default:
				t.Fatalf("stat path = %q", path)
				return nil, nil
			}
		},
		status: func(_ context.Context, sock string) (*ipnstate.Status, error) {
			if want := filepath.Join(svc.runDir, "tailscaled.sock"); sock != want {
				t.Fatalf("socket = %q, want %q", sock, want)
			}
			statusCalls++
			return tailscaleStatusWithIP(), nil
		},
		verifyMount: func(probe ResolverMountProbe) error {
			pid := lifecycleProbePID(t, probe)
			verifiedPIDs = append(verifiedPIDs, pid)
			if probe.SourcePath != fmt.Sprintf("/proc/%d/root%s", tailscaledPID, filepath.Join(svc.runDir, "resolv.conf")) {
				t.Fatalf("resolver source = %q", probe.SourcePath)
			}
			return nil
		},
		wait: func(_ context.Context, delay time.Duration) error {
			waits++
			if delay != 100*time.Millisecond {
				t.Fatalf("sample delay = %s, want 100ms", delay)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("VerifyTailscaleSidecar returned error: %v", err)
	}
	if pidCalls != 3 {
		t.Fatalf("MainPID calls = %d, want 3", pidCalls)
	}
	if statusCalls != 2 {
		t.Fatalf("LocalAPI calls = %d, want 2; transitional Catch PID must fail before LocalAPI", statusCalls)
	}
	if waits != 2 {
		t.Fatalf("verification waits = %d, want 2", waits)
	}
	if got := fmt.Sprint(verifiedPIDs); got != "[202 202]" {
		t.Fatalf("verified resolver PIDs = %s, want [202 202]", got)
	}
}

func TestVerifyTailscaleSidecarRequiresStablePID(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	pids := []int{101, 202, 101, 202, 202}
	var calls int
	err := svc.verifyTailscaleSidecar(context.Background(), tailscaleLifecycleDeps{
		mainPID: func(string) (int, error) {
			pid := pids[calls]
			calls++
			return pid, nil
		},
		stat:        lifecycleMatchingStat(svc, 101, 202),
		status:      lifecycleReadyStatus,
		verifyMount: func(ResolverMountProbe) error { return nil },
		wait:        func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("VerifyTailscaleSidecar returned error: %v", err)
	}
	if calls != len(pids) {
		t.Fatalf("MainPID calls = %d, want %d; changing PID must reset the stable sample count", calls, len(pids))
	}
}

func TestVerifyTailscaleSidecarRejectsStaleSocket(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stale := errors.New("stale tailscaled socket")
	err := svc.verifyTailscaleSidecar(ctx, tailscaleLifecycleDeps{
		mainPID:     func(string) (int, error) { return 101, nil },
		stat:        lifecycleMatchingStat(svc, 101),
		status:      func(context.Context, string) (*ipnstate.Status, error) { return nil, stale },
		verifyMount: func(ResolverMountProbe) error { return nil },
		wait:        lifecycleContextWait,
	})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, stale) {
		t.Fatalf("VerifyTailscaleSidecar error = %v, want deadline wrapping stale socket error", err)
	}
}

func TestVerifyTailscaleSidecarRejectsWrongExecutable(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := svc.verifyTailscaleSidecar(ctx, tailscaleLifecycleDeps{
		mainPID:     func(string) (int, error) { return 101, nil },
		stat:        lifecycleDifferentExecutableStat(svc, 101),
		status:      lifecycleReadyStatus,
		verifyMount: func(ResolverMountProbe) error { return nil },
		wait:        lifecycleContextWait,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match installed tailscaled binary") {
		t.Fatalf("VerifyTailscaleSidecar error = %v, want executable mismatch", err)
	}
}

func TestVerifyTailscaleSidecarRejectsResolverMismatch(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	mismatch := errors.New("resolver source and target do not refer to the same file")
	err := svc.verifyTailscaleSidecar(ctx, tailscaleLifecycleDeps{
		mainPID:     func(string) (int, error) { return 101, nil },
		stat:        lifecycleMatchingStat(svc, 101),
		status:      lifecycleReadyStatus,
		verifyMount: func(ResolverMountProbe) error { return mismatch },
		wait:        lifecycleContextWait,
	})
	if !errors.Is(err, mismatch) {
		t.Fatalf("VerifyTailscaleSidecar error = %v, want resolver mismatch", err)
	}
}

func TestVerifyTailscaleSidecarRejectsWritableResolver(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	writable := errors.New("resolver mount point /etc/resolv.conf is not read-only")
	err := svc.verifyTailscaleSidecar(ctx, tailscaleLifecycleDeps{
		mainPID:     func(string) (int, error) { return 101, nil },
		stat:        lifecycleMatchingStat(svc, 101),
		status:      lifecycleReadyStatus,
		verifyMount: func(ResolverMountProbe) error { return writable },
		wait:        lifecycleContextWait,
	})
	if !errors.Is(err, writable) {
		t.Fatalf("VerifyTailscaleSidecar error = %v, want writable resolver error", err)
	}
}

func TestVerifyTailscaleSidecarHonorsCancellation(t *testing.T) {
	svc := newTailscaleLifecycleService(t)
	ctx, cancel := context.WithCancel(context.Background())
	missingIP := errors.New("tailscale has no IPs yet")
	err := svc.verifyTailscaleSidecar(ctx, tailscaleLifecycleDeps{
		mainPID:     func(string) (int, error) { return 101, nil },
		stat:        lifecycleMatchingStat(svc, 101),
		status:      func(context.Context, string) (*ipnstate.Status, error) { return nil, missingIP },
		verifyMount: func(ResolverMountProbe) error { return nil },
		wait: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, missingIP) {
		t.Fatalf("VerifyTailscaleSidecar error = %v, want cancellation wrapping %v", err, missingIP)
	}
}

func TestRestartTailscaleSidecarVerifiesFinalProcess(t *testing.T) {
	tmp := t.TempDir()
	systemctlLog := installFakeSystemctl(t, tmp)
	svc := newTailscaleLifecycleService(t)

	oldDeps := tailscaleLifecycleDepsFn
	tailscaleLifecycleDepsFn = func() tailscaleLifecycleDeps {
		return tailscaleLifecycleDeps{
			mainPID:     func(string) (int, error) { return 101, nil },
			stat:        lifecycleMatchingStat(svc, 101),
			status:      lifecycleReadyStatus,
			verifyMount: func(ResolverMountProbe) error { return nil },
			wait:        func(context.Context, time.Duration) error { return nil },
		}
	}
	t.Cleanup(func() { tailscaleLifecycleDepsFn = oldDeps })

	if err := svc.RestartTailscaleSidecar(context.Background()); err != nil {
		t.Fatalf("RestartTailscaleSidecar returned error: %v", err)
	}
	if got := readSystemctlLog(t, systemctlLog); fmt.Sprint(got) != "[restart yeet-demo-ts.service]" {
		t.Fatalf("systemctl calls = %v, want restart followed by final-process verification", got)
	}
}

func TestTailscaleResolverVerificationFailureStopsReplacement(t *testing.T) {
	for _, action := range []string{"start", "restart"} {
		t.Run(action, func(t *testing.T) {
			tmp := t.TempDir()
			systemctlLog := installFakeSystemctl(t, tmp)
			service := newTailscaleLifecycleService(t)
			verificationErr := errors.New("final sidecar verification failed")
			oldDeps := tailscaleLifecycleDepsFn
			tailscaleLifecycleDepsFn = func() tailscaleLifecycleDeps {
				return tailscaleLifecycleDeps{
					mainPID: func(string) (int, error) {
						return 0, verificationErr
					},
					stat:        os.Stat,
					status:      lifecycleReadyStatus,
					verifyMount: func(ResolverMountProbe) error { return nil },
					wait: func(context.Context, time.Duration) error {
						return verificationErr
					},
				}
			}
			t.Cleanup(func() { tailscaleLifecycleDepsFn = oldDeps })

			var err error
			if action == "start" {
				err = service.StartTailscaleSidecar(context.Background())
			} else {
				err = service.RestartTailscaleSidecar(context.Background())
			}
			if !errors.Is(err, verificationErr) {
				t.Fatalf("%s error = %v, want verification error %v", action, err, verificationErr)
			}
			want := fmt.Sprintf("[%s yeet-demo-ts.service stop yeet-demo-ts.service]", action)
			if got := fmt.Sprint(readSystemctlLog(t, systemctlLog)); got != want {
				t.Fatalf("systemctl calls = %s, want %s", got, want)
			}
		})
	}
}

func TestRestartTailscaleSidecarVerifiesHistoricalManagedDaemonFromGuardedUnit(t *testing.T) {
	tmp := t.TempDir()
	systemctlLog := installFakeSystemctl(t, tmp)
	svc := newTailscaleLifecycleService(t)
	root := filepath.Dir(svc.runDir)
	currentDaemon := filepath.Join(root, "bin", "tailscaled")
	historicalDaemon := filepath.Join(root, "run", "tailscaled")
	if err := os.WriteFile(historicalDaemon, []byte("historical tailscaled"), 0o755); err != nil {
		t.Fatalf("write historical tailscaled: %v", err)
	}
	if err := os.Remove(currentDaemon); err != nil {
		t.Fatalf("remove current tailscaled: %v", err)
	}
	catchRunner := filepath.Join(root, "run", "catch")
	unit := "[Service]\n" +
		"ExecStart=" + catchRunner + " tailscale-resolver-exec --source " + filepath.Join(svc.runDir, "resolv.conf") + " -- " + historicalDaemon + " --statedir=. --tun=ts0\n" +
		"BindReadOnlyPaths=" + filepath.Join(svc.runDir, "resolv.conf") + ":/etc/resolv.conf\n"
	if err := os.WriteFile(svc.tailscaledServicePath(), []byte(unit), 0o644); err != nil {
		t.Fatalf("write guarded historical unit: %v", err)
	}

	const pid = 303
	var statPaths []string
	var waits int
	stopRedLoop := errors.New("stop after repeated failed historical verification")
	oldDeps := tailscaleLifecycleDepsFn
	tailscaleLifecycleDepsFn = func() tailscaleLifecycleDeps {
		return tailscaleLifecycleDeps{
			mainPID: func(string) (int, error) { return pid, nil },
			stat: func(path string) (os.FileInfo, error) {
				statPaths = append(statPaths, path)
				switch path {
				case historicalDaemon, fmt.Sprintf("/proc/%d/exe", pid):
					return os.Stat(historicalDaemon)
				case catchRunner:
					t.Fatalf("verification compared the process with the Catch launcher %s", path)
					return nil, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			status:      lifecycleReadyStatus,
			verifyMount: func(ResolverMountProbe) error { return nil },
			wait: func(context.Context, time.Duration) error {
				waits++
				if waits == 1 {
					return nil
				}
				return stopRedLoop
			},
		}
	}
	t.Cleanup(func() { tailscaleLifecycleDepsFn = oldDeps })

	if err := svc.RestartTailscaleSidecar(context.Background()); err != nil {
		t.Fatalf("RestartTailscaleSidecar returned error: %v; stat paths = %v", err, statPaths)
	}
	if got := readSystemctlLog(t, systemctlLog); fmt.Sprint(got) != "[restart yeet-demo-ts.service]" {
		t.Fatalf("systemctl calls = %v, want historical sidecar restart", got)
	}
	for _, path := range statPaths {
		if path == currentDaemon {
			t.Fatalf("verification stat current daemon %s even though installed unit retained %s", currentDaemon, historicalDaemon)
		}
	}
}

func TestVerifyTailscaleSidecarRejectsUnmanagedUnitDaemon(t *testing.T) {
	for _, tt := range []struct {
		daemon string
		want   string
	}{
		{daemon: "run/tailscaled", want: "absolute clean path"},
		{daemon: "/service/data/tailscaled", want: "tailscaled path"},
		{daemon: "/service/run/not-tailscaled", want: "tailscaled path"},
	} {
		t.Run(tt.daemon, func(t *testing.T) {
			svc := newTailscaleLifecycleService(t)
			unit := "[Service]\nExecStart=" + tt.daemon + " --tun=ts0\n" +
				"BindReadOnlyPaths=" + filepath.Join(svc.runDir, "resolv.conf") + ":/etc/resolv.conf\n"
			if err := os.WriteFile(svc.tailscaledServicePath(), []byte(unit), 0o644); err != nil {
				t.Fatalf("write unmanaged unit: %v", err)
			}
			_, err := svc.verifyTailscaleSidecarSample(context.Background(), tailscaleLifecycleDeps{
				mainPID:     func(string) (int, error) { return 101, nil },
				stat:        lifecycleMatchingStat(svc, 101),
				status:      lifecycleReadyStatus,
				verifyMount: func(ResolverMountProbe) error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("verifyTailscaleSidecarSample unit daemon %q error = %v, want %q", tt.daemon, err, tt.want)
			}
		})
	}
}

func TestVerifyTailscaleSidecarRejectsWrongGuardLauncher(t *testing.T) {
	for _, launcherKind := range []string{"arbitrary", "current tailscaled", "historical tailscaled"} {
		t.Run(launcherKind, func(t *testing.T) {
			svc := newTailscaleLifecycleService(t)
			root := filepath.Dir(svc.runDir)
			current := filepath.Join(root, "bin", "tailscaled")
			historical := filepath.Join(root, "run", "tailscaled")
			if err := os.WriteFile(historical, []byte("historical"), 0o755); err != nil {
				t.Fatalf("write historical tailscaled: %v", err)
			}
			launcher := "/bin/echo"
			switch launcherKind {
			case "current tailscaled":
				launcher = current
			case "historical tailscaled":
				launcher = historical
			}
			unit := "[Service]\nExecStart=" + launcher +
				" tailscale-resolver-exec --source " + filepath.Join(svc.runDir, "resolv.conf") +
				" -- " + current + " --statedir=. --tun=ts0\n"
			if err := os.WriteFile(svc.tailscaledServicePath(), []byte(unit), 0o644); err != nil {
				t.Fatalf("write guarded unit: %v", err)
			}

			_, err := svc.verifyTailscaleSidecarSample(context.Background(), tailscaleLifecycleDeps{
				mainPID:     func(string) (int, error) { return 101, nil },
				stat:        lifecycleMatchingStat(svc, 101),
				status:      lifecycleReadyStatus,
				verifyMount: func(ResolverMountProbe) error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), "guard launcher") {
				t.Fatalf("verifyTailscaleSidecarSample guard launcher %q error = %v, want guard launcher rejection", launcher, err)
			}
		})
	}
}

func TestVerifyTailscaleSidecarRequiresExpectedGuardRunner(t *testing.T) {
	for _, tt := range []struct {
		name           string
		expectedRunner string
	}{
		{name: "missing"},
		{name: "mismatched", expectedRunner: "/srv/other/run/catch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTailscaleLifecycleService(t)
			root := filepath.Dir(svc.runDir)
			current := filepath.Join(root, "bin", "tailscaled")
			stableRunner := filepath.Join(root, "run", "catch")
			svc.tailscaleGuardRunner = tt.expectedRunner
			unit := "[Service]\nExecStart=" + stableRunner +
				" tailscale-resolver-exec --source " + filepath.Join(svc.runDir, "resolv.conf") +
				" -- " + current + " --statedir=. --tun=ts0\n"
			if err := os.WriteFile(svc.tailscaledServicePath(), []byte(unit), 0o644); err != nil {
				t.Fatalf("write guarded unit: %v", err)
			}

			_, err := svc.verifyTailscaleSidecarSample(context.Background(), tailscaleLifecycleDeps{
				mainPID:     func(string) (int, error) { return 101, nil },
				stat:        lifecycleMatchingStat(svc, 101),
				status:      lifecycleReadyStatus,
				verifyMount: func(ResolverMountProbe) error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), "expected stable Catch runner") {
				t.Fatalf("verifyTailscaleSidecarSample expected runner %q error = %v, want fail-closed rejection", tt.expectedRunner, err)
			}
		})
	}
}

func TestTailscaledDaemonFromUnitStrictGrammar(t *testing.T) {
	const root = "/srv/demo"
	current := filepath.Join(root, "bin", "tailscaled")
	historical := filepath.Join(root, "run", "tailscaled")
	stableCatchRunner := "/srv/catch/run/catch"
	for _, tt := range []struct {
		name                string
		unit                string
		expectedCatchRunner string
		want                string
	}{
		{
			name: "valid direct current",
			unit: "[Service]\nExecStart=" + current + " --statedir=.\n",
			want: current,
		},
		{
			name:                "valid guarded current",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/netns/yeet-demo-ns/resolv.conf -- " + current + " --statedir=.\n",
			expectedCatchRunner: stableCatchRunner,
			want:                current,
		},
		{
			name:                "valid guarded historical",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/netns/yeet-demo-ns/resolv.conf -- " + historical + " --statedir=.\n",
			expectedCatchRunner: stableCatchRunner,
			want:                historical,
		},
		{
			name: "multiple exec starts",
			unit: "[Service]\nExecStart=" + current + "\nExecStart=" + historical + "\n",
		},
		{
			name: "empty exec start",
			unit: "[Service]\nExecStart=\n",
		},
		{
			name: "exec start outside service",
			unit: "[Unit]\nExecStart=" + current + "\n[Service]\n",
		},
		{
			name: "quoted directive",
			unit: "[Service]\nExecStart=\"" + current + "\"\n",
		},
		{
			name: "continued directive",
			unit: "[Service]\nExecStart=" + current + " \\\n --statedir=.\n",
		},
		{
			name:                "malformed guarded source flag",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --resolver /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "malformed guarded separator",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name: "relative executable",
			unit: "[Service]\nExecStart=run/tailscaled\n",
		},
		{
			name: "unclean absolute executable",
			unit: "[Service]\nExecStart=/srv/demo/bin/../bin/tailscaled\n",
		},
		{
			name:                "guarded unmanaged daemon",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- /srv/demo/data/tailscaled\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "arbitrary guard launcher",
			unit:                "[Service]\nExecStart=/bin/echo tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "current daemon as guard launcher",
			unit:                "[Service]\nExecStart=" + current + " tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "historical daemon as guard launcher",
			unit:                "[Service]\nExecStart=" + historical + " tailscale-resolver-exec --source /etc/resolv.conf -- " + current + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "versioned guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/catch-20260725 tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "install staging guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/.install/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "relative guard launcher",
			unit:                "[Service]\nExecStart=run/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "unclean guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/../run/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "wrong basename guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/not-catch tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name: "missing expected catch runner",
			unit: "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
		},
		{
			name:                "mismatched expected catch runner",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: "/srv/other/run/catch",
		},
		{
			name:                "relative expected catch runner",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: "run/catch",
		},
		{
			name:                "unclean expected catch runner",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: "/srv/catch/run/../run/catch",
		},
		{
			name:                "wrong basename expected catch runner",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: "/srv/catch/run/catch-current",
		},
		{
			name:                "install staging expected catch runner",
			unit:                "[Service]\nExecStart=/srv/catch/run/.install/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + historical + "\n",
			expectedCatchRunner: "/srv/catch/run/.install/catch",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TailscaledDaemonFromUnit(tt.unit, root, tt.expectedCatchRunner)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("tailscaled daemon = %q, want strict grammar rejection", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("TailscaledDaemonFromUnit: %v", err)
			}
			if got != tt.want {
				t.Fatalf("tailscaled daemon = %q, want %q", got, tt.want)
			}
		})
	}
}

func newTailscaleLifecycleService(t *testing.T) *SystemdService {
	t.Helper()
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	systemdDir := filepath.Join(root, "systemd")
	for _, dir := range []string{runDir, systemdDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewSystemdService(
		nil,
		(&db.Service{Name: "demo", Generation: 1, Artifacts: db.ArtifactStore{db.ArtifactNetNSService: artifactAt(1, "netns.service")}}).View(),
		runDir,
		WithTailscaleGuardRunner(filepath.Join(root, "run", "catch")),
	)
	if err != nil {
		t.Fatalf("NewSystemdService: %v", err)
	}
	svc.systemdDir = systemdDir
	binaryPath := svc.artifactInstaller()[db.ArtifactTSBinary].dstPath
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{binaryPath, filepath.Join(filepath.Dir(binaryPath), "catch")} {
		if err := os.WriteFile(path, []byte(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unit := "[Service]\nExecStart=" + binaryPath + " --statedir=. --tun=ts0\n" +
		"BindReadOnlyPaths=" + filepath.Join(runDir, "resolv.conf") + ":/etc/resolv.conf\n"
	if err := os.WriteFile(svc.tailscaledServicePath(), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	return svc
}

func lifecycleMatchingStat(svc *SystemdService, pids ...int) func(string) (os.FileInfo, error) {
	return func(path string) (os.FileInfo, error) {
		if path == svc.artifactInstaller()[db.ArtifactTSBinary].dstPath {
			return os.Stat(path)
		}
		for _, pid := range pids {
			if path == fmt.Sprintf("/proc/%d/exe", pid) {
				return os.Stat(svc.artifactInstaller()[db.ArtifactTSBinary].dstPath)
			}
		}
		return nil, fmt.Errorf("unexpected stat path %s", path)
	}
}

func lifecycleDifferentExecutableStat(svc *SystemdService, pid int) func(string) (os.FileInfo, error) {
	wrongPath := filepath.Join(filepath.Dir(svc.runDir), "bin", "catch")
	return func(path string) (os.FileInfo, error) {
		if path == svc.artifactInstaller()[db.ArtifactTSBinary].dstPath {
			return os.Stat(path)
		}
		if path == fmt.Sprintf("/proc/%d/exe", pid) {
			return os.Stat(wrongPath)
		}
		return nil, fmt.Errorf("unexpected stat path %s", path)
	}
}

func lifecycleReadyStatus(context.Context, string) (*ipnstate.Status, error) {
	return tailscaleStatusWithIP(), nil
}

func tailscaleStatusWithIP() *ipnstate.Status {
	return &ipnstate.Status{TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}}
}

func lifecycleContextWait(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func lifecycleProbePID(t *testing.T, probe ResolverMountProbe) int {
	t.Helper()
	var pid int
	if _, err := fmt.Sscanf(probe.TargetPath, "/proc/%d/root/etc/resolv.conf", &pid); err != nil {
		t.Fatalf("resolver target = %q: %v", probe.TargetPath, err)
	}
	if probe.MountInfoPath != fmt.Sprintf("/proc/%d/mountinfo", pid) || probe.MountPoint != "/etc/resolv.conf" {
		t.Fatalf("resolver probe = %#v", probe)
	}
	return pid
}
