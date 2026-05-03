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
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/client/local"
)

type pushFlagsParsed struct {
	Run      bool `flag:"run"`
	AllLocal bool `flag:"all-local"`
}

type pushRequest struct {
	Service  string
	Image    string
	Tag      string
	AllLocal bool
}

func HandlePush(ctx context.Context, args []string) error {
	req, err := parsePushRequest(args)
	if err != nil {
		return err
	}
	goos, goarch, err := remoteCatchOSAndArch()
	if err != nil {
		return err
	}
	if req.AllLocal {
		return pushAllLocalImages(req.Service, goos, goarch)
	}
	return pushImage(ctx, req.Service, req.Image, req.Tag)
}

func parsePushRequest(args []string) (pushRequest, error) {
	if len(args) == 0 {
		return pushRequest{}, errors.New("missing svc argument")
	}
	if args[0] == "push" {
		args = args[1:]
	}
	result, err := yargs.ParseFlags[pushFlagsParsed](args)
	if err != nil {
		return pushRequest{}, err
	}
	pos := append([]string{}, result.Args...)
	if len(result.RemainingArgs) > 0 {
		pos = append(pos, result.RemainingArgs...)
	}
	if len(pos) < 1 {
		return pushRequest{}, errors.New("missing svc argument")
	}
	req := pushRequest{Service: pos[0], AllLocal: result.Flags.AllLocal}
	if result.Flags.AllLocal {
		return req, nil
	}
	if len(pos) < 2 {
		return pushRequest{}, errors.New("missing image argument")
	}
	req.Image = pos[1]
	req.Tag = "latest"
	if result.Flags.Run {
		req.Tag = "run"
	}
	return req, nil
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

type dockerBuild struct {
	Args []string
}

func buildDockerImageForRemote(ctx context.Context, dockerfilePath, imageName string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found")
	}
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	build, err := dockerBuildPlan(dockerfilePath, imageName, goos, goarch)
	if err != nil {
		return err
	}
	return runDockerBuild(ctx, build)
}

func dockerBuildPlan(dockerfilePath, imageName, goos, goarch string) (dockerBuild, error) {
	if goos != "linux" {
		return dockerBuild{}, fmt.Errorf("remote host is not running linux: %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return dockerBuild{}, fmt.Errorf("remote host is running an unsupported architecture: %s", goarch)
	}
	targetPlatform := fmt.Sprintf("linux/%s", goarch)
	dockerfileDir := filepath.Dir(dockerfilePath)
	return dockerBuild{Args: []string{
		"build",
		"--platform", targetPlatform,
		"-t", imageName,
		"-f", dockerfilePath,
		dockerfileDir,
	}}, nil
}

func runDockerBuild(ctx context.Context, build dockerBuild) error {
	cmd := exec.CommandContext(ctx, "docker", build.Args...)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(output)); msg != "" {
			fmt.Fprintf(os.Stderr, "\nDocker build error:\n%s\n", msg)
		}
		return fmt.Errorf("docker %s: %w", strings.Join(build.Args, " "), err)
	}
	return nil
}

func pushImage(ctx context.Context, _ string, image, tag string) error {
	return pushImageWithDeps(ctx, image, tag, pushImageDeps{
		host:        getDockerHost,
		imageExists: imageExists,
		push:        runDockerPush,
	})
}

type pushImageDeps struct {
	host        func(context.Context) (string, error)
	imageExists func(string) bool
	push        func(source, target string) error
}

func pushImageWithDeps(ctx context.Context, image, tag string, deps pushImageDeps) error {
	host, err := deps.host(ctx)
	if err != nil {
		return err
	}
	if !deps.imageExists(image) {
		return fmt.Errorf("image %s does not exist", image)
	}
	imgName, err := pushTargetImageName(host, image, tag)
	if err != nil {
		return err
	}
	return deps.push(image, imgName)
}

func pushTargetImageName(host, image, tag string) (string, error) {
	repo, err := pushRepoName(image)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s:%s", host, repo, tag), nil
}

func pushRepoName(image string) (string, error) {
	repo := image
	if i := strings.LastIndex(repo, ":"); i >= 0 {
		repo = repo[:i]
	}
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 2 {
		if strings.ContainsAny(parts[0], ".:") {
			repo = parts[1]
		}
	}
	if strings.Count(repo, "/") > 1 {
		return "", fmt.Errorf("invalid image name %q - repo must be in format 'svc' or 'svc/container'", image)
	}
	return repo, nil
}

func runDockerPush(source, target string) error {
	return do(
		exec.Command("docker", "tag", source, target).Run,
		cmdutil.NewStdCmd("docker", "push", target).Run,
		exec.Command("docker", "rmi", target).Run,
	)
}

func pushAllLocalImages(s, goos, goarch string) error {
	images, err := listLocalImages(s)
	if err != nil {
		return err
	}
	for _, image := range images {
		if err := pushLocalImageIfCompatible(s, image, goos, goarch); err != nil {
			return err
		}
	}
	return nil
}

func listLocalImages(s string) ([]string, error) {
	wild := fmt.Sprintf("%s/%s/*", svc.InternalRegistryHost, s)
	if _, err := exec.LookPath("docker"); err != nil {
		log.Printf("docker not found, skipping push of local images")
		return nil, nil
	}
	cmd := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", fmt.Sprintf("reference=%s", wild))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if bytes.Contains(output, []byte("Is the docker daemon running?")) {
			log.Printf("docker daemon not running, skipping push of local images")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list images: %w (%s)", err, output)
	}
	return localImagesFromDockerOutput(output), nil
}

func localImagesFromDockerOutput(output []byte) []string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil
	}
	images := strings.Split(trimmed, "\n")
	out := make([]string, 0, len(images))
	for _, image := range images {
		if image == "" {
			continue
		}
		out = append(out, image)
	}
	return out
}

func pushLocalImageIfCompatible(s, image, goos, goarch string) error {
	sys, arch, err := imageSystemAndArch(image)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipping, failed to get image arch for %q: %v\n", image, err)
		return nil
	}
	shouldPush, skip := localImagePushDecision(image, sys, arch, goos, goarch)
	if !shouldPush {
		fmt.Fprintln(os.Stderr, skip)
		return nil
	}
	if err := pushImage(context.Background(), s, image, "latest"); err != nil {
		return err
	}
	return nil
}

func localImagePushDecision(image, sys, arch, goos, goarch string) (bool, string) {
	if sys != goos {
		return false, fmt.Sprintf("skipping, image %q is for (local) %s, not (remote) %s", image, sys, goos)
	}
	if goarch != arch {
		return false, fmt.Sprintf("skipping, image %q is for (local) %s, not (remote) %s", image, arch, goarch)
	}
	return true, ""
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
