// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/fatih/color"
	"github.com/hugomd/ascii-live/frames"
	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/catch"
	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
	"github.com/yeetrun/yeet/pkg/svc"
	"golang.org/x/term"
	"tailscale.com/client/tailscale"
)

var (
	prefsFile       = filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
	bridgedArgs     []string
	serviceOverride string
	remoteRegistry  = cli.RemoteCommandRegistry()
)

const (
	defaultHost    = "catch"
	defaultRPCPort = 41548
)

func init() {
	if err := loadedPrefs.load(); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("failed to load preferences: %v", err)
		}
	}
	if host := os.Getenv("CATCH_HOST"); host != "" {
		loadedPrefs.Host = host
	}
	if port := os.Getenv("CATCH_RPC_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			loadedPrefs.RPCPort = p
		}
	}
	if loadedPrefs.Host == "" {
		loadedPrefs.Host = defaultHost
	}
	if loadedPrefs.RPCPort == 0 {
		loadedPrefs.RPCPort = defaultRPCPort
	}
}

var loadedPrefs prefs

type prefs struct {
	changed bool   `json:"-"`
	Host    string `json:"host"`
	RPCPort int    `json:"rpcPort"`
}

type globalFlagsParsed struct {
	Host    string `flag:"host" help:"Override target host (CATCH_HOST)"`
	Service string `flag:"service" help:"Force the service name for the command"`
	RPCPort int    `flag:"rpc-port" help:"Override RPC port (CATCH_RPC_PORT)"`
}

func parseGlobalFlags(args []string) (globalFlagsParsed, []string, error) {
	result, err := yargs.ParseKnownFlags[globalFlagsParsed](args, yargs.KnownFlagsOptions{})
	if err != nil {
		return globalFlagsParsed{}, nil, err
	}
	return result.Flags, result.RemainingArgs, nil
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

func asJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func newRPCClient(host string) *catchrpc.Client {
	return catchrpc.NewClient(host, loadedPrefs.RPCPort)
}

func watchResize(ctx context.Context, fd int) <-chan catchrpc.Resize {
	ch := make(chan catchrpc.Resize, 4)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		defer close(ch)
		defer signal.Stop(sigCh)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				cols, rows, err := term.GetSize(fd)
				if err != nil {
					continue
				}
				ch <- catchrpc.Resize{Rows: rows, Cols: cols}
			}
		}
	}()
	return ch
}

func payloadNameFromReader(r io.Reader) string {
	if r == nil {
		return ""
	}
	type namer interface {
		Name() string
	}
	n, ok := r.(namer)
	if !ok {
		return ""
	}
	name := strings.TrimSpace(n.Name())
	if name == "" {
		return ""
	}
	base := filepath.Base(name)
	if base == "." || base == string(os.PathSeparator) || base == ".." {
		return ""
	}
	return base
}

type errorPrefixer interface {
	errorPrefix() string
}

type remoteExitError struct {
	code   int
	prefix string
}

func (e remoteExitError) Error() string {
	return fmt.Sprintf("remote exit %d", e.code)
}

func (e remoteExitError) errorPrefix() string {
	return e.prefix
}

type trackingWriter struct {
	w    io.Writer
	last byte
	saw  bool
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		t.last = p[len(p)-1]
		t.saw = true
	}
	return t.w.Write(p)
}

func (t *trackingWriter) LastByte() (byte, bool) {
	return t.last, t.saw
}

func errorPrefixForRemoteExit(rawMode bool, lastByte byte, sawOutput bool) string {
	if !rawMode || !sawOutput {
		return ""
	}
	switch lastByte {
	case '\n':
		return "\r"
	case '\r':
		return "\n"
	default:
		return "\r\n"
	}
}

func printCLIError(w io.Writer, err error) {
	if err == nil {
		return
	}
	var pref errorPrefixer
	if errors.As(err, &pref) {
		if prefix := pref.errorPrefix(); prefix != "" {
			fmt.Fprint(w, prefix)
		}
	}
	fmt.Fprintln(w, err)
}

func execRemote(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
	client := newRPCClient(loadedPrefs.Host)
	req := catchrpc.ExecRequest{
		Service: service,
		Args:    args,
		TTY:     tty,
	}
	if stdin != nil && stdin != os.Stdin {
		if payload := payloadNameFromReader(stdin); payload != "" {
			req.PayloadName = payload
		}
	}
	var resizeCh <-chan catchrpc.Resize
	fd := int(os.Stdin.Fd())
	rawMode := false
	if tty && isTerminalFn(fd) {
		cols, rows, err := term.GetSize(fd)
		if err == nil {
			req.Cols = cols
			req.Rows = rows
		}
		req.Term = os.Getenv("TERM")
		if stdin == nil || stdin == os.Stdin {
			state, err := term.MakeRaw(fd)
			if err == nil {
				rawMode = true
				defer term.Restore(fd, state)
				resizeCh = watchResize(ctx, fd)
			} else {
				req.TTY = false
			}
		} else {
			resizeCh = watchResize(ctx, fd)
		}
	} else {
		req.TTY = false
	}
	if stdin == nil && req.TTY {
		stdin = os.Stdin
	}
	out := &trackingWriter{w: os.Stdout}
	code, err := client.Exec(ctx, req, stdin, out, resizeCh)
	if err != nil {
		return err
	}
	if code != 0 {
		last, saw := out.LastByte()
		prefix := errorPrefixForRemoteExit(rawMode && isTerminalFn(int(os.Stderr.Fd())), last, saw)
		return remoteExitError{code: code, prefix: prefix}
	}
	return nil
}

var execRemoteFn = execRemote
var remoteCatchOSAndArchFn = remoteCatchOSAndArch
var pushAllLocalImagesFn = pushAllLocalImages
var isTerminalFn = term.IsTerminal

type namedReadCloser struct {
	io.ReadCloser
	name string
}

func (n *namedReadCloser) Name() string {
	return n.name
}

func openPayloadForUpload(file, goos, goarch string) (io.ReadCloser, func(), ftdetect.FileType, error) {
	ft, err := ftdetect.DetectFile(file, goos, goarch)
	if err != nil {
		return nil, nil, ftdetect.Unknown, fmt.Errorf("failed to detect file type: %w", err)
	}
	if ft != ftdetect.Binary {
		f, err := os.Open(file)
		if err != nil {
			return nil, nil, ft, err
		}
		return f, func() { f.Close() }, ft, nil
	}

	tmpPattern := fmt.Sprintf("yeet-zstd-%s-*.zst", filepath.Base(file))
	tmpFile, err := os.CreateTemp("", tmpPattern)
	if err != nil {
		return nil, nil, ft, err
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, nil, ft, err
	}
	if err := codecutil.ZstdCompress(file, tmpPath); err != nil {
		os.Remove(tmpPath)
		return nil, nil, ft, err
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return nil, nil, ft, err
	}
	payload := &namedReadCloser{ReadCloser: f, name: filepath.Base(file)}
	cleanup := func() {
		payload.Close()
		os.Remove(tmpPath)
	}
	return payload, cleanup, ft, nil
}

func handleEventsRPC(ctx context.Context, svc string, flags cli.EventsFlags) error {
	sub := catchrpc.EventsRequest{All: flags.All}
	if !flags.All {
		sub.Service = svc
	}
	return newRPCClient(loadedPrefs.Host).Events(ctx, sub, func(ev catchrpc.Event) {
		fmt.Fprintf(os.Stdout, "Received event: %v\n", ev)
	})
}

func main() {
	args := os.Args[1:]
	globalFlags, remaining, err := parseGlobalFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if globalFlags.Host != "" {
		if globalFlags.Host != loadedPrefs.Host {
			loadedPrefs.Host = globalFlags.Host
			loadedPrefs.changed = true
		}
	}
	if globalFlags.Service != "" {
		serviceOverride = globalFlags.Service
	}
	if globalFlags.RPCPort != 0 && globalFlags.RPCPort != loadedPrefs.RPCPort {
		loadedPrefs.RPCPort = globalFlags.RPCPort
		loadedPrefs.changed = true
	}
	helpConfig := buildHelpConfig()
	args = yargs.ApplyAliases(remaining, helpConfig)
	args = rewriteEnvSetArgs(args)

	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	if len(args) > 1 {
		if svc, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, serviceOverride); ok {
			serviceOverride = svc
			bridgedArgs = bridged
			args = bridged
		}
	}

	handlers := make(map[string]yargs.SubcommandHandler)
	for _, name := range cli.RemoteCommandNames() {
		handlers[name] = handleRemote
	}
	handlers["mount"] = handleMountSys
	handlers["umount"] = handleMountSys
	handlers["init"] = handleInit
	handlers["list-hosts"] = handleListHosts
	handlers["prefs"] = handlePrefs
	handlers["skirt"] = handleSkirt

	// Keep group handlers aligned with pkg/cli/cli.go metadata and
	// cmd/yeet/cli_bridge.go localGroupCommands.
	groups := buildGroupHandlers()
	if err := yargs.RunSubcommandsWithGroups(context.Background(), args, helpConfig, globalFlagsParsed{}, handlers, groups); err != nil {
		printCLIError(os.Stderr, err)
	}
}

func handleListHosts(ctx context.Context, args []string) error {
	var lc tailscale.LocalClient
	st, err := lc.Status(ctx)
	if err != nil {
		return err
	}
	_, selfDomain, _ := strings.Cut(st.Self.DNSName, ".")

	flags, err := parseListHostsFlags(args)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "HOST\tVERSION\tTAGS")

	for _, peer := range st.Peer {
		if peer.Tags == nil || !overlaps(peer.Tags.AsSlice(), flags.Tags) {
			continue
		}
		host, domain, _ := strings.Cut(peer.DNSName, ".")
		if domain != selfDomain {
			continue
		}
		rpc := newRPCClient(host)
		infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var info catch.ServerInfo
		if err := rpc.Call(infoCtx, "catch.Info", nil, &info); err != nil {
			log.Printf("failed to get version for %s: %v", host, err)
			fmt.Fprintf(w, "%s\t%s\t%s\n", host, "unknown", strings.Join(peer.Tags.AsSlice(), ","))
			cancel()
			continue
		}
		cancel()
		fmt.Fprintf(w, "%s\t%s\t%s\n", host, info.Version, strings.Join(peer.Tags.AsSlice(), ","))
	}
	return nil
}

type listHostsFlags struct {
	Tags []string
}

type listHostsFlagsParsed struct {
	Tags []string `flag:"tags"`
}

func parseListHostsFlags(args []string) (listHostsFlags, error) {
	flags := listHostsFlags{Tags: []string{"tag:catch"}}
	if len(args) == 0 {
		return flags, nil
	}
	if args[0] == "list-hosts" {
		args = args[1:]
	}
	result, err := yargs.ParseKnownFlags[listHostsFlagsParsed](args, yargs.KnownFlagsOptions{SplitCommaSlices: true})
	if err != nil {
		return flags, err
	}
	if len(result.Flags.Tags) > 0 {
		flags.Tags = result.Flags.Tags
	}
	return flags, nil
}

type pushFlagsParsed struct {
	Run      bool `flag:"run"`
	AllLocal bool `flag:"all-local"`
}

func handlePush(ctx context.Context, args []string) error {
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

type prefsFlagsParsed struct {
	Save bool `flag:"save"`
}

func handlePrefs(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "prefs" {
		args = args[1:]
	}
	result, err := yargs.ParseFlags[prefsFlagsParsed](args)
	if err != nil {
		return err
	}
	fmt.Println(asJSON(loadedPrefs))
	if result.Flags.Save {
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
}

func handleInit(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "init" {
		args = args[1:]
	}
	if len(args) == 0 {
		return updateCatch()
	}
	if len(args) > 1 {
		return fmt.Errorf("init takes at most one argument")
	}
	return initCatch(args[0])
}

func handleSkirt(ctx context.Context, _ []string) error {
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
		fmt.Print("\033[H\033[2J")
		x++
		i := x % p.GetLength()
		c := colors[x%len(colors)]
		c.Println(p.GetFrame(i))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(p.GetSleep()):
			continue
		}
	}
}

func handleRemote(_ context.Context, args []string) error {
	if len(bridgedArgs) > 0 {
		return handleSvcCmdFn(bridgedArgs)
	}
	return handleSvcCmdFn(args)
}

func handleDockerGroup(_ context.Context, args []string) error {
	full := append([]string{"docker"}, args...)
	return handleRemote(nil, full)
}

func handleEnvGroup(_ context.Context, args []string) error {
	full := append([]string{"env"}, args...)
	return handleRemote(nil, full)
}

func rewriteEnvSetArgs(args []string) []string {
	if len(args) < 3 {
		return args
	}
	if args[0] != "env" {
		return args
	}
	switch args[1] {
	case "show", "edit", "copy", "set":
		return args
	}
	if !strings.Contains(args[2], "=") {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, "env", "set", args[1])
	out = append(out, args[2:]...)
	return out
}

func buildGroupHandlers() map[string]yargs.Group {
	return map[string]yargs.Group{
		"docker": {
			Description: "Docker compose and registry management",
			Commands: map[string]yargs.SubcommandHandler{
				"pull":   handleDockerGroup,
				"update": handleDockerGroup,
				"push":   handlePush,
			},
		},
		"env": {
			Description: "Manage service environment files",
			Commands: map[string]yargs.SubcommandHandler{
				"show": handleEnvGroup,
				"edit": handleEnvGroup,
				"copy": handleEnvGroup,
				"set":  handleEnvGroup,
			},
		},
	}
}

func handleMountSys(ctx context.Context, _ []string) error {
	return execRemote(ctx, catch.SystemService, os.Args[1:], nil, true)
}

func buildHelpConfig() yargs.HelpConfig {
	subcommands := make(map[string]yargs.SubCommandInfo)
	for name, info := range cli.RemoteCommandInfos() {
		subcommands[name] = yargs.SubCommandInfo{
			Name:        name,
			Description: info.Description,
			Usage:       info.Usage,
			Examples:    info.Examples,
			Hidden:      info.Hidden,
			Aliases:     info.Aliases,
		}
	}
	subcommands["init"] = yargs.SubCommandInfo{
		Name:        "init",
		Description: "Install catch on a remote host",
		Usage:       "ROOT@HOST",
		Examples:    []string{"yeet init root@<host>", "yeet init"},
	}
	subcommands["list-hosts"] = yargs.SubCommandInfo{
		Name:        "list-hosts",
		Description: "List all hosts with the given tags",
		Usage:       "[--tags=tag:catch]",
	}
	subcommands["prefs"] = yargs.SubCommandInfo{
		Name:        "prefs",
		Description: "Manage the current preferences",
	}
	subcommands["skirt"] = yargs.SubCommandInfo{
		Name:   "skirt",
		Hidden: true,
	}
	groups := make(map[string]yargs.GroupInfo)
	for name, info := range cli.RemoteGroupInfos() {
		commands := make(map[string]yargs.SubCommandInfo)
		for sub, cmd := range info.Commands {
			commands[sub] = yargs.SubCommandInfo{
				Name:        cmd.Name,
				Description: cmd.Description,
				Usage:       cmd.Usage,
				Examples:    cmd.Examples,
				Hidden:      cmd.Hidden,
				Aliases:     cmd.Aliases,
			}
		}
		groups[name] = yargs.GroupInfo{
			Name:        info.Name,
			Description: info.Description,
			Commands:    commands,
			Hidden:      info.Hidden,
		}
	}
	if docker, ok := groups["docker"]; ok {
		docker.Commands["push"] = yargs.SubCommandInfo{
			Name:        "push",
			Description: "Push a container image to the remote host (optionally run it)",
			Usage:       "docker push SVC IMAGE [--run] [--all-local]",
			Examples:    []string{"yeet docker push <svc> <local-image>:<tag> --run"},
		}
		groups["docker"] = docker
	}
	return yargs.HelpConfig{
		Command: yargs.CommandInfo{
			Name:        "yeet",
			Description: "Deploy and manage services on a remote catch host; most commands are forwarded over RPC on your tailnet.",
			Examples: []string{
				"yeet status",
				"yeet status <svc>",
				"yeet run <svc> ./bin/<svc> -- --app-flag value",
				"yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app",
			},
		},
		SubCommands: subcommands,
		Groups:      groups,
	}
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
	if serviceOverride != "" {
		return serviceOverride
	}
	return catch.SystemService
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var si catch.ServerInfo
	if err := newRPCClient(loadedPrefs.Host).Call(ctx, "catch.Info", nil, &si); err != nil {
		return "", "", fmt.Errorf("failed to get version of catch binary: %w", err)
	}
	return si.GOOS, si.GOARCH, nil
}

func updateCatch() error {
	return initCatch(loadedPrefs.Host)
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
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	payload, cleanup, _, err := openPayloadForUpload(bin, goos, goarch)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := execRemoteFn(context.Background(), svc, []string{"stage"}, payload, false); err != nil {
		return fmt.Errorf("failed to upload file %s to stage: %w", bin, err)
	}
	return nil
}

func missingServiceError(args []string) error {
	name := missingServiceCommandName(args)
	if name == "" {
		return fmt.Errorf("missing service name")
	}
	return fmt.Errorf("%s requires a service name\nRun 'yeet %s --help' for usage", name, name)
}

func missingServiceCommandName(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) > 1 {
		if _, ok := cli.RemoteGroupInfos()[args[0]]; ok {
			return args[0] + " " + args[1]
		}
	}
	return args[0]
}

func commandNeedsService(args []string) (bool, error) {
	res, ok, err := yargs.ResolveCommandWithRegistry(args, remoteRegistry)
	if err != nil || !ok {
		return false, err
	}
	if len(res.Path) > 0 && res.Path[0] == cli.CommandEvents {
		flags, _, err := cli.ParseEvents(args[1:])
		if err != nil {
			return false, err
		}
		if flags.All {
			return false, nil
		}
	}
	arg, ok := res.PArg(0)
	if !ok {
		return false, nil
	}
	if !cli.IsServiceArgSpec(arg) {
		return false, nil
	}
	return arg.Required, nil
}

func handleSvcCmd(args []string) error {
	svc := getService()
	if len(args) == 0 {
		return execRemoteFn(context.Background(), svc, []string{"status"}, nil, true)
	}
	if serviceOverride == "" {
		needsService, err := commandNeedsService(args)
		if err != nil {
			return err
		}
		if needsService {
			return missingServiceError(args)
		}
	}

	// Check for special commands
	switch args[0] {
	case "env":
		if len(args) >= 2 && args[1] == "copy" {
			if len(args) != 3 {
				return fmt.Errorf("env copy requires a file")
			}
			return runEnvCopy(args[2])
		}
		if len(args) >= 2 && args[1] == "set" {
			if len(args) < 3 {
				return fmt.Errorf("env set requires at least one KEY=VALUE assignment")
			}
			assignments, err := parseEnvAssignments(args[2:])
			if err != nil {
				return err
			}
			svc := getService()
			setArgs := []string{"env", "set"}
			for _, assignment := range assignments {
				setArgs = append(setArgs, assignment.Key+"="+assignment.Value)
			}
			return execRemoteFn(context.Background(), svc, setArgs, nil, true)
		}
	// `run <svc> <file/docker-image> [args...]`
	case "run":
		if len(args) >= 2 {
			payload, runArgs, err := splitRunPayloadArgs(args[1:])
			if err != nil {
				return err
			}
			return runRun(payload, runArgs)
		}
		return fmt.Errorf("run requires a payload")
	// `copy <svc> <file> [dest]`
	case "copy":
		switch len(args) {
		case 2:
			return runCopy(args[1], "")
		case 3:
			return runCopy(args[1], args[2])
		}
		return fmt.Errorf("copy requires a source file and optional destination")
	// `cron <svc> <file> <cronexpr>`
	case "cron":
		return runCron(args[1], args[2:])
	// `stage <svc> <file>`
	case "stage":
		if len(args) == 2 {
			return runStageBinary(args[1])
		}
	case cli.CommandEvents:
		flags, _, err := cli.ParseEvents(args[1:])
		if err != nil {
			return err
		}
		if serviceOverride == "" && !flags.All {
			return missingServiceError(args)
		}
		return handleEventsRPC(context.Background(), svc, flags)
	}

	// Assume the first argument is a command
	return execRemoteFn(context.Background(), svc, args, nil, true)
}

var handleSvcCmdFn = handleSvcCmd
var tryRunDockerFn = tryRunDocker
var buildDockerImageForRemoteFn = buildDockerImageForRemote
var tryRunRemoteImageFn = tryRunRemoteImage

func splitRunPayloadArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("run requires a payload")
	}
	specs := cli.RemoteFlagSpecs()["run"]
	payloadIdx := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			name := arg
			if idx := strings.Index(name, "="); idx != -1 {
				name = name[:idx]
			}
			if spec, ok := specs[name]; ok {
				if spec.ConsumesValue && !strings.Contains(arg, "=") {
					i++
				}
				continue
			}
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			if strings.Contains(arg, "=") {
				name := arg[:strings.Index(arg, "=")]
				if _, ok := specs[name]; ok {
					continue
				}
			} else if len(arg) == 2 {
				if spec, ok := specs[arg]; ok {
					if spec.ConsumesValue {
						i++
					}
					continue
				}
			} else if short := "-" + string(arg[1]); short != "-" {
				if spec, ok := specs[short]; ok && spec.ConsumesValue {
					continue
				}
			}
		}
		payloadIdx = i
		break
	}
	if payloadIdx == -1 {
		return "", nil, fmt.Errorf("run requires a payload")
	}
	payload := args[payloadIdx]
	out := make([]string, 0, len(args)-1)
	out = append(out, args[:payloadIdx]...)
	out = append(out, args[payloadIdx+1:]...)
	return payload, out, nil
}

func runRun(payload string, args []string) error {
	if ok, err := tryRunDockerfile(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunFile(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunRemoteImageFn(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunDockerFn(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown payload: %s", payload)
}

func tryRunDockerfile(path string, args []string) (ok bool, _ error) {
	if filepath.Base(path) != "Dockerfile" {
		return false, nil
	}
	if st, err := os.Stat(path); os.IsNotExist(err) || st != nil && st.IsDir() {
		return false, fmt.Errorf("Dockerfile payload does not exist: %s", path)
	} else if err != nil {
		return false, err
	}
	svc := getService()
	tag := fmt.Sprintf("yeet-build-%d", time.Now().UnixNano())
	imageName := fmt.Sprintf("%s:%s", svc, tag)
	if err := buildDockerImageForRemoteFn(context.Background(), path, imageName); err != nil {
		return true, err
	}
	ok, err := tryRunDockerFn(imageName, args)
	_ = exec.Command("docker", "rmi", imageName).Run()
	return ok, err
}

const imageComposeTemplate = `services:
  %s:
    image: %s
    restart: unless-stopped
    volumes:
      - "./:/data"
`

func tryRunRemoteImage(image string, args []string) (ok bool, _ error) {
	if !looksLikeImageRef(image) {
		return false, nil
	}
	svc := getService()
	tmpDir, err := os.MkdirTemp("", "yeet-image-")
	if err != nil {
		return true, err
	}
	defer os.RemoveAll(tmpDir)
	composePath := filepath.Join(tmpDir, "compose.yml")
	content := fmt.Sprintf(imageComposeTemplate, svc, image)
	if err := os.WriteFile(composePath, []byte(content), 0644); err != nil {
		return true, err
	}
	return runFilePayload(composePath, args, false)
}

func looksLikeImageRef(payload string) bool {
	if payload == "" {
		return false
	}
	if strings.ContainsAny(payload, " \t\n\r") {
		return false
	}
	if strings.HasPrefix(payload, "http://") || strings.HasPrefix(payload, "https://") {
		return false
	}
	if strings.Contains(payload, "@") {
		parts := strings.SplitN(payload, "@", 2)
		return parts[0] != "" && parts[1] != ""
	}
	lastSlash := strings.LastIndex(payload, "/")
	lastColon := strings.LastIndex(payload, ":")
	if lastColon == -1 || lastColon < lastSlash {
		return false
	}
	tag := payload[lastColon+1:]
	return tag != "" && !strings.Contains(tag, "/")
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
	return runFilePayload(file, args, true)
}

func runFilePayload(file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return false, err
	}
	payload, cleanup, ft, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return false, err
	}
	svc := getService()
	if ft == ftdetect.DockerCompose && pushLocalImages {
		if err := pushAllLocalImagesFn(svc, goos, goarch); err != nil {
			return false, fmt.Errorf("failed to push all local images: %w", err)
		}
	}
	defer cleanup()
	runArgs := append([]string{"run"}, args...)
	tty := isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteFn(context.Background(), svc, runArgs, payload, tty); err != nil {
		return false, fmt.Errorf("failed to run service: %w", err)
	}
	return true, nil
}

func runCopy(file, dest string) error {
	if file == "" {
		return fmt.Errorf("copy requires a source file")
	}
	if st, err := os.Stat(file); err != nil {
		return err
	} else if st.IsDir() {
		return fmt.Errorf("%q is a directory, expected a file", file)
	}
	normalized, err := normalizeCopyDest(file, dest)
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	svc := getService()
	args := []string{"copy", normalized}
	if err := execRemoteFn(context.Background(), svc, args, f, false); err != nil {
		return err
	}
	return nil
}

func normalizeCopyDest(src, dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	trimmed := strings.TrimPrefix(dest, "./")
	if trimmed == "" {
		trimmed = filepath.Base(src)
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("copy destination must be relative")
	}

	rel := trimmed
	if rel == "data" || strings.HasPrefix(rel, "data/") {
		rel = strings.TrimPrefix(rel, "data")
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" || strings.HasSuffix(dest, "/") || strings.HasSuffix(rel, "/") {
		base := filepath.Base(src)
		if base == "." || base == string(os.PathSeparator) {
			return "", fmt.Errorf("invalid source file %q", src)
		}
		rel = filepath.Join(rel, base)
	}

	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid copy destination %q", dest)
	}
	return filepath.Join("data", rel), nil
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
		stageArgs := append([]string{"stage"}, args...)
		if err := execRemote(context.Background(), svc, stageArgs, nil, true); err != nil {
			fmt.Println("failed to stage args:", err)
			return false, fmt.Errorf("failed to stage args: %w", err)
		}
	}
	// Run stage commit (don't inherit os.Args)
	if err := execRemote(context.Background(), svc, []string{"stage", "commit"}, nil, true); err != nil {
		return false, errors.New("failed to run service")
	}
	return true, nil
}

func runEnvCopy(file string) error {
	if file == "" {
		return fmt.Errorf("env copy requires a file")
	}
	if st, err := os.Stat(file); err != nil {
		return err
	} else if st.IsDir() {
		return fmt.Errorf("%q is a directory, expected a file", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	svc := getService()
	args := []string{"env", "copy"}
	if err := execRemoteFn(context.Background(), svc, args, f, false); err != nil {
		return err
	}
	return nil
}

type envAssignment struct {
	Key   string
	Value string
}

func parseEnvAssignments(args []string) ([]envAssignment, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("env set requires at least one KEY=VALUE assignment")
	}
	seen := make(map[string]int, len(args))
	assignments := make([]envAssignment, 0, len(args))
	for _, arg := range args {
		key, value, err := splitEnvAssignment(arg)
		if err != nil {
			return nil, err
		}
		if idx, ok := seen[key]; ok {
			assignments[idx].Value = value
			continue
		}
		seen[key] = len(assignments)
		assignments = append(assignments, envAssignment{Key: key, Value: value})
	}
	return assignments, nil
}

func splitEnvAssignment(arg string) (string, string, error) {
	i := strings.Index(arg, "=")
	if i <= 0 {
		return "", "", fmt.Errorf("invalid env assignment %q (expected KEY=VALUE)", arg)
	}
	key := arg[:i]
	value := arg[i+1:]
	if strings.TrimSpace(key) != key {
		return "", "", fmt.Errorf("invalid env key %q (contains whitespace)", key)
	}
	if !isValidEnvKey(key) {
		return "", "", fmt.Errorf("invalid env key %q", key)
	}
	return key, value, nil
}

func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				return false
			}
			continue
		}
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
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

func runCron(file string, args []string) error {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	payload, cleanup, _, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return err
	}
	defer cleanup()
	svc := getService()
	cronArgs, binArgs, err := splitCronArgs(args)
	if err != nil {
		return err
	}
	nargs := append([]string{"cron"}, cronArgs...)
	if len(binArgs) > 0 {
		nargs = append(nargs, binArgs...)
	}
	tty := isTerminalFn(int(os.Stdout.Fd()))
	return execRemoteFn(context.Background(), svc, nargs, payload, tty)
}

func splitCronArgs(args []string) ([]string, []string, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("cron requires a cron expression")
	}
	cronArgs := args
	var binArgs []string
	for i, arg := range args {
		if arg == "--" {
			cronArgs = args[:i]
			if i+1 < len(args) {
				binArgs = args[i+1:]
			}
			break
		}
	}
	if len(cronArgs) == 1 {
		cronArgs = strings.Fields(cronArgs[0])
	}
	if len(cronArgs) != 5 {
		return nil, nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(cronArgs))
	}
	return cronArgs, binArgs, nil
}

func runStageBinary(file string) error {
	svc := getService()
	if st, err := os.Stat(file); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return execRemote(context.Background(), svc, []string{"stage", file}, nil, true)
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
