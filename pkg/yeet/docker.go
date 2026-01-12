// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shayne/yargs"
	"github.com/shayne/yeet/pkg/cmdutil"
	"github.com/shayne/yeet/pkg/svc"
	"tailscale.com/client/local"
)

type pushFlagsParsed struct {
	Run      bool `flag:"run"`
	AllLocal bool `flag:"all-local"`
}

func HandlePush(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("missing svc argument")
	}
	if args[0] == "push" {
		args = args[1:]
	}
	result, err := yargs.ParseFlags[pushFlagsParsed](args)
	if err != nil {
		return err
	}
	pos := append([]string{}, result.Args...)
	if len(result.RemainingArgs) > 0 {
		pos = append(pos, result.RemainingArgs...)
	}
	if len(pos) < 1 {
		return errors.New("missing svc argument")
	}
	goos, goarch, err := remoteCatchOSAndArch()
	if err != nil {
		return err
	}
	svc := pos[0]
	if result.Flags.AllLocal {
		return pushAllLocalImages(svc, goos, goarch)
	}
	if len(pos) < 2 {
		return errors.New("missing image argument")
	}
	image := pos[1]
	tag := "latest"
	if result.Flags.Run {
		tag = "run"
	}
	return pushImage(ctx, svc, image, tag)
}

func getDockerHost(ctx context.Context) (string, error) {
	var lc local.Client
	st, err := lc.Status(ctx)
	if err != nil {
		return "", err
	}
	for _, peer := range st.Peer {
		// Check for FQDN match
		if strings.EqualFold(strings.TrimSuffix(peer.DNSName, "."), Host()) {
			return strings.TrimSuffix(peer.DNSName, "."), nil
		}
		// Check for shortname match
		h, _, _ := strings.Cut(peer.DNSName, ".")
		if strings.EqualFold(h, Host()) {
			return strings.TrimSuffix(peer.DNSName, "."), nil
		}
	}
	return "", fmt.Errorf("host not found")
}

func do(f ...func() error) error {
	for _, fn := range f {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

func imageExists(imageName string) bool {
	// Execute the Docker command to list images
	cmd := exec.Command("docker", "images", "-q", imageName)
	output, err := cmd.Output()

	// If there's an error or no output, the image doesn't exist
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return false
	}
	return true
}

func buildDockerImageForRemote(ctx context.Context, dockerfilePath, imageName string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found")
	}
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	if goos != "linux" {
		return fmt.Errorf("remote host is not running linux: %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("remote host is running an unsupported architecture: %s", goarch)
	}
	targetPlatform := fmt.Sprintf("linux/%s", goarch)
	dockerfileDir := filepath.Dir(dockerfilePath)
	args := []string{
		"build",
		"--platform", targetPlatform,
		"-t", imageName,
		"-f", dockerfilePath,
		dockerfileDir,
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(output)); msg != "" {
			fmt.Fprintf(os.Stderr, "\nDocker build error:\n%s\n", msg)
		}
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func pushImage(ctx context.Context, svc, image, tag string) error {
	host, err := getDockerHost(ctx)
	if err != nil {
		return err
	}
	// Check if the image already exists locally.
	if !imageExists(image) {
		return fmt.Errorf("image %s does not exist", image)
	}
	// Extract the repo from the image name
	repo := image
	// Strip tag if present
	if i := strings.LastIndex(repo, ":"); i >= 0 {
		repo = repo[:i]
	}
	// Strip registry host if present
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 2 {
		// Check if the first part is a registry host by looking for . or : characters
		// This matches Docker's reference parsing logic
		if strings.ContainsAny(parts[0], ".:") {
			repo = parts[1]
		}
	}
	// Validate repo format
	if strings.Count(repo, "/") > 1 {
		return fmt.Errorf("invalid image name %q - repo must be in format 'svc' or 'svc/container'", image)
	}

	// Format of <fqdn>/<svc>/<svc>:<tag>
	imgName := fmt.Sprintf("%s/%s:%s", host, repo, tag)
	if err := do(
		exec.Command("docker", "tag", image, imgName).Run,
		cmdutil.NewStdCmd("docker", "push", imgName).Run,
		exec.Command("docker", "rmi", imgName).Run,
	); err != nil {
		return err
	}
	return nil
}

func pushAllLocalImages(s, goos, goarch string) error {
	wild := fmt.Sprintf("%s/%s/*", svc.InternalRegistryHost, s)
	if _, err := exec.LookPath("docker"); err != nil {
		log.Printf("docker not found, skipping push of local images")
		return nil
	}
	cmd := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", fmt.Sprintf("reference=%s", wild))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if bytes.Contains(output, []byte("Is the docker daemon running?")) {
			log.Printf("docker daemon not running, skipping push of local images")
			return nil
		}
		return fmt.Errorf("failed to list images: %w (%s)", err, output)
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil
	}
	images := strings.Split(trimmed, "\n")
	for _, image := range images {
		if image == "" {
			continue
		}
		sys, arch, err := imageSystemAndArch(image)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping, failed to get image arch for %q: %v\n", image, err)
			continue
		}
		if sys != goos {
			fmt.Fprintf(os.Stderr, "skipping, image %q is for (local) %s, not (remote) %s\n", image, sys, goos)
			continue
		}
		if goarch != arch {
			fmt.Fprintf(os.Stderr, "skipping, image %q is for (local) %s, not (remote) %s\n", image, arch, goarch)
			continue
		}
		if err := pushImage(context.Background(), s, image, "latest"); err != nil {
			return err
		}
	}
	return nil
}

func imageSystemAndArch(image string) (system, arch string, _ error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.Os}},{{.Architecture}}", image)
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect image: %w", err)
	}
	system, arch, _ = strings.Cut(strings.TrimSpace(string(output)), ",")
	return system, arch, nil
}
