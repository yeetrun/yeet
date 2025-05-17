// Copyright 2025 AUTHORS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yeetrun/yeet/pkg/catch"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
	"github.com/yeetrun/yeet/pkg/svc"
	"github.com/fatih/color"
	"github.com/hugomd/ascii-live/frames"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"tailscale.com/client/tailscale"
)

var (
	rootCmd   *cobra.Command // Root `yeet` command
	prefsFile = filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
)

const defaultHost = "catch"

func init() {
	if err := loadedPrefs.load(); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("failed to load preferences: %v", err)
		}
	}
	if host := os.Getenv("CATCH_HOST"); host != "" {
		loadedPrefs.Host = host
	}
	if loadedPrefs.Host == "" {
		loadedPrefs.Host = defaultHost
	}
}

var loadedPrefs prefs

type prefs struct {
	changed bool   `json:"-"`
	Host    string `json:"host"`
}

type flagPref[T comparable] struct {
	t       *T
	changed *bool
	typ     string
}

func (fp flagPref[T]) Set(v T) error {
	if *fp.t == v {
		return nil
	}
	*fp.t = v
	*fp.changed = true
	return nil
}

func (fp flagPref[T]) Type() string {
	if fp.typ != "" {
		return fp.typ
	}
	return "string"
}

func (fp flagPref[T]) String() string {
	return fmt.Sprint(*fp.t)
}

func (p *prefs) HostValue() pflag.Value {
	return flagPref[string]{t: &p.Host, changed: &p.changed}
}

func (p *prefs) save() error {
	if err := os.MkdirAll(filepath.Dir(prefsFile), 0755); err != nil {
		return err
	}
	j, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(prefsFile, j, 0600)
}

func (p *prefs) load() error {
	fp := filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
	j, err := os.ReadFile(fp)
	if err != nil {
		return err
	}
	return json.Unmarshal(j, p)
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

func getDockerHost(ctx context.Context) (string, error) {
	var lc tailscale.LocalClient
	st, err := lc.Status(ctx)
	if err != nil {
		return "", err
	}
	for _, peer := range st.Peer {
		// Check for FQDN match
		if strings.EqualFold(strings.TrimSuffix(peer.DNSName, "."), loadedPrefs.Host) {
			return strings.TrimSuffix(peer.DNSName, "."), nil
		}
		// Check for shortname match
		h, _, _ := strings.Cut(peer.DNSName, ".")
		if strings.EqualFold(h, loadedPrefs.Host) {
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

type clientReadWriter struct {
	in  io.Reader
	out io.Writer
}

func (c *clientReadWriter) Read(p []byte) (n int, err error) {
	return c.in.Read(p)
}

func (c *clientReadWriter) Write(p []byte) (n int, err error) {
	return c.out.Write(p)
}

func asJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func main() {
	rw := &clientReadWriter{in: os.Stdin, out: os.Stdout}
	h := cli.NewCommandHandler(rw, run)
	rootCmd = h.RootCmd("yeet")
	rootCmd.PersistentFlags().Var(loadedPrefs.HostValue(), "host", "remote host to connect to")

	// Collect all the commands from the cli package to determine which need the
	// service flag
	var remoteCmds []string
	for _, cmd := range rootCmd.Commands() {
		remoteCmds = append(remoteCmds, strings.Split(cmd.Use, " ")[0])
	}

	// Create and hide a service flag to plumb the service name through
	rootCmd.PersistentFlags().String("service", "", "hidden service flag")
	rootCmd.PersistentFlags().MarkHidden("service")

	// Root commands
	rootCmd.AddCommand(&cobra.Command{
		Use:   "init <remote>",
		Short: "Install catch on a remote host",
		Args:  cobra.MaximumNArgs(1),
		RunE:  run,
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:          "docker-host",
		Short:        "Print out the docker host",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := getDockerHost(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Print(host)
			return nil
		},
	})
	var pushShouldRun bool
	var pushAllLocal bool
	pushCmd := &cobra.Command{
		Use:          "push <svc> <image>",
		Short:        "Push a container image to the remote host",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			goos, goarch, err := remoteCatchOSAndArch()
			if err != nil {
				return err
			}
			svc := args[0]
			if pushAllLocal {
				return pushAllLocalImages(svc, goos, goarch)
			}
			if len(args) < 2 {
				return errors.New("missing image argument")
			}
			image := args[1]
			tag := "latest" // Default tag (does not auto-deploy)
			if pushShouldRun {
				tag = "run"
			}
			return pushImage(cmd.Context(), svc, image, tag)
		},
	}
	pushCmd.Flags().BoolVar(&pushShouldRun, "run", false, "auto-deploy the image")
	pushCmd.Flags().BoolVar(&pushAllLocal, "all-local", false, "auto-deploy the image")
	rootCmd.AddCommand(pushCmd)
	lhCmd := &cobra.Command{
		Use:   "list-hosts [--tags=tag:catch]",
		Short: "List all hosts with the given tags",
		RunE:  runListHosts,
	}
	lhCmd.PersistentFlags().StringSliceVar(&listHostsFlags.tags, "tags", []string{"tag:catch"}, "tags to filter by")
	rootCmd.AddCommand(lhCmd)

	var save bool
	prefsCmd := &cobra.Command{
		Use:   "prefs",
		Short: "Manage the current preferences",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(asJSON(loadedPrefs))
			if save {
				if !loadedPrefs.changed {
					fmt.Fprintln(os.Stderr, "No changes to save")
					return nil
				}
				if err := loadedPrefs.save(); err != nil {
					return fmt.Errorf("failed to save preferences: %w", err)
				}
				fmt.Fprintln(os.Stderr, "Prefs saved")
			} else if loadedPrefs.changed {
				fmt.Fprintln(os.Stderr, "Use --save to save the prefs")
			}
			return nil
		},
	}
	prefsCmd.PersistentFlags().BoolVar(&save, "save", false, "save the current prefs")

	rootCmd.AddCommand(prefsCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:    "skirt",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			colors := []*color.Color{
				color.New(color.FgRed),
				color.New(color.FgGreen),
				color.New(color.FgYellow),
				color.New(color.FgBlue),
				color.New(color.FgMagenta),
				color.New(color.FgCyan),
				color.New(color.FgWhite),
			}
			p := frames.Parrot
			x := 0
			for {
				fmt.Print("\033[H\033[2J") // Clear the screen

				x++
				i := x % p.GetLength()
				c := colors[x%len(colors)]

				c.Println(p.GetFrame(i))
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(p.GetSleep()):
					continue
				}
			}
		},
	})

	args := os.Args[1:]
	if len(args) > 1 && slices.Contains(remoteCmds, args[0]) {
		// Find first non flag argument and assume it's the service
		var firstArg string
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if arg == "--format" {
				i++ // Skip the next argument as it is the value for --format
				continue
			}
			if !strings.HasPrefix(arg, "-") {
				firstArg = arg
				break
			}
		}
		// If no non flag argument is found, default to "sys" service
		if firstArg == "" {
			firstArg = "sys"
		}

		// Parse any existing flags before overriding args
		rootCmd.ParseFlags(args)
		// Assume args[1] is the service
		rootCmd.ParseFlags([]string{"--service", firstArg})
		args := append([]string{args[0]}, args[2:]...)
		rootCmd.SetArgs(args)
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

var listHostsFlags struct {
	tags []string
}

func runListHosts(cmd *cobra.Command, _ []string) error {
	var lc tailscale.LocalClient
	st, err := lc.Status(cmd.Context())
	if err != nil {
		return err
	}
	_, selfDomain, _ := strings.Cut(st.Self.DNSName, ".")

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "HOST\tVERSION\tTAGS")

	for _, peer := range st.Peer {
		if peer.Tags == nil || !overlaps(peer.Tags.AsSlice(), listHostsFlags.tags) {
			continue
		}
		host, domain, _ := strings.Cut(peer.DNSName, ".")
		if domain != selfDomain {
			continue
		}
		c := cmdutil.NewStdCmd("ssh", host, "version")
		c.Stdout = nil
		version, err := c.Output()
		if err != nil {
			log.Printf("failed to get version for %s: %v", host, err)
			version = []byte("unknown")
		}
		version = bytes.TrimSpace(version)
		fmt.Fprintf(w, "%s\t%s\t%s\n", host, version, strings.Join(peer.Tags.AsSlice(), ","))
	}
	return nil
}

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

func getService() string {
	if svc, _ := rootCmd.Flags().GetString("service"); svc != "" {
		return svc
	}
	return "sys"
}

func run(cmd *cobra.Command, args []string) error {
	switch cmd.CalledAs() {
	case "init":
		// Install catch on a remote host
		if len(args) == 0 {
			return updateCatch()
		}
		remote := args[0]
		return initCatch(remote)
	case "mount", "umount":
		return sshTTYCmd("sys", os.Args[1:]...).Run()
	}
	// Assume the command is a service command
	cmds := []string{cmd.CalledAs()}
	for cmd.Parent() != cmd.Root() && cmd.Parent() != nil {
		cmd = cmd.Parent()
		cmds = append([]string{cmd.Use}, cmds...)
	}
	// Args turns into the series of subcommands plus the arguments. This is a
	// remote command, pass the args over the wire. Args consist of os.Args,
	// minus the binary, service name and any commands/subcommands.
	idx := min(len(cmds)+2, len(os.Args))
	args = append(cmds, os.Args[idx:]...)
	return handleSvcCmd(args)
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

// remoteCatchOSAndArch fetches the GOOS and GOARCH of the remote host by running the
// catch binary on the remote host. It uses SSH to run `catch version --json` on
// the remote host.
func remoteCatchOSAndArch() (goos, goarch string, _ error) {
	cmd := sshTTYCmd("catch", "version", "--json")
	cmd.Stdout = nil
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to get version of catch binary: %w: %s", err, out)
	}
	var si catch.ServerInfo
	if err := json.Unmarshal(out, &si); err != nil {
		return "", "", err
	}
	return si.GOOS, si.GOARCH, nil
}

func updateCatch() error {
	goos, goarch, err := remoteCatchOSAndArch()
	if err != nil {
		return err
	}
	catch, err := buildCatch(goos, goarch)
	if err != nil {
		return err
	}
	defer os.Remove(catch)

	// Compress the catch binary
	compressedCatch := catch + ".zst"
	if err := codecutil.ZstdCompress(catch, compressedCatch); err != nil {
		return fmt.Errorf("failed to compress catch binary: %w", err)
	}
	defer os.Remove(compressedCatch)

	f, err := os.Open(compressedCatch)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := sshTTYCmd("catch", "run")
	cmd.Stdin = f
	return cmd.Run()
}

func buildCatch(goos, goarch string) (string, error) {
	goos = strings.ToLower(goos)
	goarch = strings.ToLower(goarch)
	// Check if the system is Linux
	if goos != "linux" {
		log.Fatalf("Remote system is not Linux: %s", goos)
	}

	fmt.Println("Remote architecture:", goarch)

	// Check if we are in the git root directory
	cmd := cmdutil.NewStdCmd("git", "rev-parse", "--show-toplevel")
	cmd.Stdout = nil
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository")
	}
	// Get the output of the command and trim the whitespace
	gitRoot := strings.TrimSpace(string(output))

	// Check if we have go installed
	cmd = cmdutil.NewStdCmd("go", "version")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go is not installed")
	}
	// Build the catch binary
	cmd = cmdutil.NewStdCmd("go", "build", "-o", "catch", "./cmd/catch")
	cmd.Env = append(os.Environ(), "GOARCH="+goarch, "GOOS=linux")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to build catch binary")
	}
	return filepath.Join(gitRoot, "catch"), nil
}

func initCatch(userAtRemote string) error {
	useSudo := false
	if user, _, ok := strings.Cut(userAtRemote, "@"); !ok || user != "root" {
		fmt.Fprint(os.Stderr, color.RedString("Warning: root is required to install catch on the remote host.\nsudo will be used which may require a password.\n\n"))
		useSudo = true
	}
	systemName, goarch, err := remoteHostOSAndArch(userAtRemote)
	if err != nil {
		return err
	}
	bin, err := buildCatch(systemName, goarch)
	if err != nil {
		return err
	}
	// SCP the binary to the remote host
	cmd := cmdutil.NewStdCmd("scp", "-C", bin, fmt.Sprintf("%s:catch", userAtRemote))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to copy catch binary to remote host")
	}
	// Make the binary executable on the remote host
	cmd = cmdutil.NewStdCmd("ssh", userAtRemote, "chmod", "+x", "./catch")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to make catch binary executable on remote host")
	}
	args := append(make([]string, 0, 7), "-t", userAtRemote)
	if useSudo {
		args = append(args, "sudo")
	}
	args = append(args, "./catch", fmt.Sprintf("--tsnet-host=%v", loadedPrefs.Host), "install")

	// Run the catch binary on the remote host
	cmd = cmdutil.NewStdCmd("ssh", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run catch binary on remote host")
	}
	// Remove the catch binary from the local machine and the remote host
	return os.Remove(bin)
}

func stageFile(svc, bin string) error {
	svcAt := fmt.Sprintf("%s@%s", svc, loadedPrefs.Host)
	cmd := cmdutil.NewStdCmd("scp", "-v", bin, fmt.Sprintf("%s:stage", svcAt))
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to upload file %s to stage: %w", bin, err)
	}
	return nil
}

func handleSvcCmd(args []string) error {
	svc := getService()
	if len(args) == 0 {
		return sshTTYCmd(svc).Run()
	}

	// Check for special commands
	switch args[0] {
	// `run <svc> <file/docker-image> [args...]`
	case "run":
		if len(args) >= 2 {
			return runRun(args[1], args[2:])
		}
	// `cron <svc> <file> <cronexpr>`
	case "cron":
		return runCron(args[1], args[2:])
	// `stage <svc> <file>`
	case "stage":
		if len(args) == 2 {
			return runStageBinary(args[1])
		}
	case "events":
		return sshCmd(svc, args...).Run()
	}

	// Assume the first argument is a command
	return sshTTYCmd(svc, args...).Run()
}

func runRun(payload string, args []string) error {
	if ok, err := tryRunFile(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunDocker(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown payload: %s", payload)
}

func tryRunFile(file string, args []string) (ok bool, _ error) {
	if st, err := os.Stat(file); os.IsNotExist(err) || st != nil && st.IsDir() {
		// If the file does not exist or is a directory, it's not an error
		// (yet), it could be another deployment method (i.e. docker)
		if st != nil && st.IsDir() {
			fmt.Fprintf(os.Stderr, "%q is a directory, ignoring\n", file)
		}
		return false, nil
	} else if err != nil {
		// If it's a different error, return it
		return false, err
	}
	goos, goarch, err := remoteCatchOSAndArch()
	if err != nil {
		return false, err
	}
	ft, err := ftdetect.DetectFile(file, goos, goarch)
	if err != nil {
		return false, fmt.Errorf("failed to detect file type: %w", err)
	}
	svc := getService()
	if ft == ftdetect.DockerCompose {
		if err := pushAllLocalImages(svc, goos, goarch); err != nil {
			return false, fmt.Errorf("failed to push all local images: %w", err)
		}
	}
	if err := stageFile(svc, file); err != nil {
		fmt.Println("failed to stage file:", err)
		return false, fmt.Errorf("failed to stage file: %w", err)
	}
	// If there are more arguments, run `stage <svc> <args...>`
	if len(args) > 0 {
		args := append([]string{"stage"}, args...)
		if err := sshTTYCmd(svc, args...).Run(); err != nil {
			fmt.Println("failed to stage args:", err)
			return false, fmt.Errorf("failed to stage args: %w", err)
		}
	}
	// Run ssh svc@catch stage commit (don't inherit os.Args)
	if err := sshTTYCmd(svc, "stage", "commit").Run(); err != nil {
		return false, errors.New("failed to run service")
	}
	return true, nil
}

func tryRunDocker(image string, args []string) (ok bool, _ error) {
	if !imageExists(image) {
		// If the image does not exist, it's not an error
		return false, nil
	}
	svc := getService()
	if err := pushImage(context.Background(), svc, image, "latest"); err != nil {
		return false, fmt.Errorf("failed to push image: %w", err)
	}
	// If there are more arguments, run `stage <svc> <args...>`
	if len(args) > 0 {
		args := append([]string{"stage"}, args...)
		if err := sshTTYCmd(svc, args...).Run(); err != nil {
			fmt.Println("failed to stage args:", err)
			return false, fmt.Errorf("failed to stage args: %w", err)
		}
	}
	// Run ssh svc@catch stage commit (don't inherit os.Args)
	if err := sshTTYCmd(svc, "stage", "commit").Run(); err != nil {
		return false, errors.New("failed to run service")
	}
	return true, nil
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
	images := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(images) == 0 {
		return nil
	}
	for _, image := range images {
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

func runCron(file string, args []string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	svc := getService()
	nargs := []string{"cron"}
	if len(args) > 0 {
		// Skip the first two arguments "cron" and the file
		nargs = append(nargs, args...)
	}
	cmd := sshTTYCmd(svc, nargs...)
	cmd.Stdin = f // Set the stdin to the file
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func runStageBinary(file string) error {
	svc := getService()
	if st, err := os.Stat(file); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return sshTTYCmd(svc, "stage", file).Run()
	} else if st != nil && st.IsDir() {
		if st.IsDir() {
			fmt.Fprintf(os.Stderr, "%q is a directory, ignoring\n", file)
		}
	}
	if err := stageFile(svc, file); err != nil {
		return err
	}
	return nil
}

func sshTTYCmd(user string, args ...string) *exec.Cmd {
	svcAt := fmt.Sprintf("%s@%s", user, loadedPrefs.Host)
	args = append([]string{"-tq", svcAt}, args...)
	return cmdutil.NewStdCmd("ssh", args...)
}

func sshCmd(user string, args ...string) *exec.Cmd {
	svcAt := fmt.Sprintf("%s@%s", user, loadedPrefs.Host)
	args = append([]string{"-q", svcAt}, args...)
	return cmdutil.NewStdCmd("ssh", args...)
}
