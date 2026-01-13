// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/shayne/yargs"
)

var archMap = map[string]string{
	"x86_64":  "amd64",
	"i386":    "386",
	"i686":    "386",
	"armv7l":  "arm",
	"armv8l":  "arm64",
	"aarch64": "arm64",
	"ppc64le": "ppc64le",
	"s390x":   "s390x",
}

type initFlagsParsed struct {
	FromGithub bool `flag:"from-github"`
	Nightly    bool `flag:"nightly"`
}

type initOptions struct {
	fromGithub bool
	nightly    bool
}

func HandleInit(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "init" {
		args = args[1:]
	}
	result, err := yargs.ParseFlags[initFlagsParsed](args)
	if err != nil {
		return err
	}
	pos := append([]string{}, result.Args...)
	if len(result.RemainingArgs) > 0 {
		pos = append(pos, result.RemainingArgs...)
	}
	opts := initOptions{
		fromGithub: result.Flags.FromGithub,
		nightly:    result.Flags.Nightly,
	}
	if len(pos) == 0 {
		return updateCatch(opts)
	}
	if len(pos) > 1 {
		return fmt.Errorf("init takes at most one argument")
	}
	return initCatch(pos[0], opts)
}

// remoteHostOSAndArch returns the system and architecture of a given remote
// host/IP. It uses SSH to run `uname -s` and `uname -m` on the remote host.
// Note that this expects the remote host to be accessible via root@remote.
func remoteHostOSAndArch(userAtRemote string) (system, goarch string, _ error) {
	cmd := exec.Command("ssh", userAtRemote, "uname -s && uname -m")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("SSH command failed: %w", err)
	}
	// Split the output into system name and architecture
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("unexpected output from remote: %v", lines)
	}

	system = lines[0]
	arch := lines[1]

	goarch, ok := archMap[arch]
	if !ok {
		log.Fatalf("Unsupported architecture: %s", arch)
	}
	return
}

// remoteCatchOSAndArch fetches the GOOS and GOARCH of the remote host by calling
// the catch RPC info endpoint.
func remoteCatchOSAndArch() (goos, goarch string, _ error) {
	info, err := remoteCatchInfo()
	if err != nil {
		return "", "", err
	}
	return info.GOOS, info.GOARCH, nil
}

func remoteCatchInfo() (serverInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var si serverInfo
	if err := newRPCClient(Host()).Call(ctx, "catch.Info", nil, &si); err != nil {
		return serverInfo{}, fmt.Errorf("failed to get version of catch binary: %w", err)
	}
	return si, nil
}

func updateCatch(opts initOptions) error {
	userAtRemote := Host()
	if info, err := remoteCatchInfo(); err == nil {
		host := userAtRemote
		if info.InstallHost != "" {
			host = info.InstallHost
		}
		if strings.Contains(host, "@") {
			userAtRemote = host
		} else if info.InstallUser != "" {
			userAtRemote = fmt.Sprintf("%s@%s", info.InstallUser, host)
		} else {
			userAtRemote = host
		}
	}
	return initCatch(userAtRemote, opts)
}

func buildCatch(goos, goarch, gitRoot string) (string, int64, error) {
	goos = strings.ToLower(goos)
	goarch = strings.ToLower(goarch)
	// Check if the system is Linux
	if goos != "linux" {
		return "", 0, fmt.Errorf("remote system is not Linux: %s", goos)
	}
	if gitRoot == "" {
		return "", 0, fmt.Errorf("missing git root for catch build")
	}
	// Build the catch binary
	cmd := exec.Command("go", "build", "-o", "catch", "./cmd/catch")
	cmd.Env = append(os.Environ(), "GOARCH="+goarch, "GOOS=linux")
	cmd.Dir = gitRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > 0 {
			fmt.Fprintln(os.Stderr, string(out))
		}
		return "", 0, fmt.Errorf("failed to build catch binary")
	}
	bin := filepath.Join(gitRoot, "catch")
	info, err := os.Stat(bin)
	if err != nil {
		return "", 0, fmt.Errorf("failed to stat catch binary: %w", err)
	}
	return bin, info.Size(), nil
}

func initCatch(userAtRemote string, opts initOptions) error {
	useSudo := false
	if user, _, ok := strings.Cut(userAtRemote, "@"); !ok || user != "root" {
		fmt.Fprint(os.Stderr, color.RedString("Warning: root is required to install catch on the remote host.\nsudo will be used which may require a password.\n\n"))
		useSudo = true
	}
	isTTY := isTerminalFn(int(os.Stdout.Fd()))
	enabled, quiet := initProgressSettings(execProgressMode(), isTTY)
	ui := newInitUI(os.Stdout, enabled, quiet, Host(), userAtRemote, catchServiceName)
	ui.Start()
	defer ui.Stop()

	ui.StartStep("Check local")
	useGithub := opts.fromGithub
	var gitRoot string
	var goVersion string
	if useGithub {
		ui.DoneStep("using GitHub release")
	} else {
		root, ok, err := localCatchRepoRoot()
		if err != nil {
			ui.FailStep(err.Error())
			return err
		}
		if !ok {
			useGithub = true
			ui.DoneStep("no local checkout; using GitHub release")
		} else {
			gitRoot = root
			goVersion, err = localGoVersion(gitRoot)
			if err != nil {
				ui.FailStep(err.Error())
				return err
			}
			ui.DoneStep(goVersion)
		}
	}

	if isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stderr.Fd())) {
		ui.StartStep("Verify SSH")
		ui.Suspend()
		err := preflightSSHHostKey(userAtRemote)
		ui.Resume()
		if err != nil {
			ui.FailStep(err.Error())
			return err
		}
		ui.DoneStep("")
	}

	ui.StartStep("Detect host")
	systemName, goarch, err := remoteHostOSAndArch(userAtRemote)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(fmt.Sprintf("%s/%s", strings.ToLower(systemName), goarch))

	if useGithub {
		ui.StartStep("Download catch")
		assetName, assetURL, shaURL, tag, err := resolveCatchReleaseAsset(systemName, goarch, opts.nightly)
		if err != nil {
			ui.FailStep(err.Error())
			return err
		}
		downloadDetail, err := downloadCatchRelease(ui, userAtRemote, assetName, assetURL, shaURL)
		if err != nil {
			ui.FailStep("download failed")
			return err
		}
		ui.DoneStep(fmt.Sprintf("%s (%s)", downloadDetail, tag))
	} else {
		ui.StartStep("Build catch")
		bin, binSize, err := buildCatch(systemName, goarch, gitRoot)
		if err != nil {
			ui.FailStep(err.Error())
			return err
		}
		defer os.Remove(bin)
		buildTarget := fmt.Sprintf("%s/%s", strings.ToLower(systemName), goarch)
		buildDetail := buildTarget
		if strings.TrimSpace(goVersion) != "" {
			buildDetail = fmt.Sprintf("%s -> %s", goVersion, buildTarget)
		}
		if binSize > 0 {
			buildDetail = fmt.Sprintf("%s, %s", buildDetail, humanReadableBytes(float64(binSize)))
		}
		ui.DoneStep(buildDetail)

		ui.StartStep("Upload catch")
		uploadDetail, err := uploadCatchBinary(ui, bin, binSize, userAtRemote)
		if err != nil {
			ui.FailStep("upload failed")
			return fmt.Errorf("failed to copy catch binary to remote host: %w", err)
		}
		ui.DoneStep(uploadDetail)
	}

	ui.StartStep("Install catch")
	cmd := exec.Command("ssh", userAtRemote, "chmod", "+x", "./catch")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		ui.FailStep("chmod failed")
		return fmt.Errorf("failed to make catch binary executable on remote host")
	}

	args := append(make([]string, 0, 7), "-t", userAtRemote)
	if useSudo {
		args = append(args, "sudo")
	}
	installEnv := []string{}
	if user, host, ok := strings.Cut(userAtRemote, "@"); ok {
		if user != "" {
			installEnv = append(installEnv, "CATCH_INSTALL_USER="+user)
		}
		if host != "" {
			installEnv = append(installEnv, "CATCH_INSTALL_HOST="+host)
		}
	} else if userAtRemote != "" {
		installEnv = append(installEnv, "CATCH_INSTALL_HOST="+userAtRemote)
	}
	if len(installEnv) > 0 {
		args = append(args, "env")
		args = append(args, installEnv...)
	}
	args = append(args, "./catch", fmt.Sprintf("--tsnet-host=%v", Host()), "install")

	// Run the catch binary on the remote host
	cmd = exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	ui.Suspend()
	filter := newInitInstallFilter(os.Stdout)
	cmd.Stdout = filter
	cmd.Stderr = filter
	if err := cmd.Run(); err != nil {
		ui.FailStep("install failed")
		return fmt.Errorf("failed to run catch binary on remote host")
	}
	installDetail := filter.SummaryDetail()
	ui.DoneStep(installDetail)
	if warn := filter.WarningSummary(); warn != "" {
		ui.Warn(warn)
	}
	if info := filter.InfoSummary(); info != "" {
		ui.Info(info)
	}
	return nil
}

func localCatchRepoRoot() (string, bool, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", false, nil
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", false, nil
	}
	gitRoot := strings.TrimSpace(string(output))
	if gitRoot == "" {
		return "", false, nil
	}
	catchDir := filepath.Join(gitRoot, "cmd", "catch")
	if info, err := os.Stat(catchDir); err != nil || !info.IsDir() {
		return "", false, nil
	}
	return gitRoot, true, nil
}

func localGoVersion(gitRoot string) (string, error) {
	cmd := exec.Command("go", "version")
	cmd.Dir = gitRoot
	goVersionOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go is not installed (required to build catch)")
	}
	return strings.TrimSpace(string(goVersionOut)), nil
}

func preflightSSHHostKey(userAtRemote string) error {
	cmd := exec.Command("ssh", userAtRemote, "true")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh preflight failed: %w", err)
	}
	return nil
}
