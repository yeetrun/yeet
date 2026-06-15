// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/catch"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	cdb "github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/dnet"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
	"tailscale.com/util/must"
)

const (
	defaultTSNetPort = 41547 // above CATCH on QWERTY
	defaultRPCPort   = 41548
)

var (
	legacyDataDir = flag.String("data-dir", must.Get(filepath.Abs("data")), "data directory")
	tsnetHost     = flag.String("tsnet-host", "catch", "hostname to use for tsnet")
	tsnetPort     = flag.Int("tsnet-port", defaultTSNetPort, "port to use for tsnet")

	// TODO: This should be randomly assigned at stored in the JSON DB.
	registryInternalAddr = flag.String("registry-internal-addr", "127.0.0.1:0", "address for registry to listen on internally")
	containerdSocket     = flag.String("containerd-socket", "/run/containerd/containerd.sock", "path to containerd socket (required for registry cache)")
)

var (
	ipv4Loopback = netip.MustParseAddr("127.0.0.1")
	ipv6Loopback = netip.MustParseAddr("::1")
)

var (
	runVMConsoleProxy                       = catch.RunVMConsoleProxy
	runDNSFn                                = catch.RunDNSServer
	exitProcess                             = os.Exit
	setupDockerFn                           = setupDocker
	setupVMHostFn                           = setupVMHost
	doInstallFn                             = doInstall
	validateCatchRuntimeFn                  = validateCatchRuntime
	ensureContainerdSnapshotterForInstallFn = ensureContainerdSnapshotterForInstall
	generateCatchTailscaleAuthKeyFn         = catch.GenerateTailscaleAuthKeyFromSecret
	writeCatchTailscaleClientSecretFn       = catch.WriteCatchTailscaleClientSecret
)

const defaultDockerConfigPath = "/etc/docker/daemon.json"
const defaultCatchTag = "tag:catch"
const oldDockerInstallDNS = "192.168.100.1"
const oldDockerInstallDNSSearch = "yeet.internal"

var errCatchTSNetUntagged = errors.New("catch Tailscale node must be tagged")

// initTSNet initializes and returns a tsnet.Server if tsnetHost is set.
func initTSNet(dataDir string) (*tsnet.Server, error) {
	if *tsnetHost == "" {
		return nil, nil
	}
	authKey, err := prepareCatchTSNetAuth(dataDir)
	if err != nil {
		return nil, err
	}
	ts := newTSNetServer(dataDir)
	return startTSNetServer(ts, authKey)
}

func startTSNetServer(ts *tsnet.Server, authKey string) (*tsnet.Server, error) {
	if authKey != "" {
		ts.AuthKey = authKey
	}
	st, err := ts.Up(context.Background())
	if err != nil {
		return nil, err
	}
	if err := validateStartedTSNetStatus(ts, st); err != nil {
		return nil, err
	}
	logStartedTSNetStatus(st)
	registerTSNetFallback(ts, tsnetIPsFromStatus(st))
	return ts, nil
}

func validateStartedTSNetStatus(ts closeErrorer, st *ipnstate.Status) error {
	if err := validateCatchTSNetSelf(tsnetSelfFromStatus(st)); err != nil {
		if closeErr := ts.Close(); closeErr != nil {
			log.Printf("warning: failed to close untagged tsnet server: %v", closeErr)
		}
		return err
	}
	return nil
}

func validateCatchTSNetSelf(self *ipnstate.PeerStatus) error {
	if self == nil || !self.IsTagged() {
		return fmt.Errorf("%w; configure Tailscale tagOwners so the catch node can use a server tag such as tag:catch, then rerun yeet init; for unattended installs pass --ts-auth-key=<key>; see https://yeetrun.com/docs/concepts/tailscale", errCatchTSNetUntagged)
	}
	return nil
}

func logStartedTSNetStatus(st *ipnstate.Status) {
	self := tsnetSelfFromStatus(st)
	if self == nil || self.DNSName == "" {
		return
	}
	log.Printf("tsnet assigned DNS name %q", self.DNSName)
	if warning := tsnetAssignedNameWarning(*tsnetHost, self.DNSName); warning != "" {
		log.Print(warning)
	}
}

func tsnetSelfFromStatus(st *ipnstate.Status) *ipnstate.PeerStatus {
	if st == nil {
		return nil
	}
	return st.Self
}

func tsnetIPsFromStatus(st *ipnstate.Status) []netip.Addr {
	if st == nil {
		return nil
	}
	return st.TailscaleIPs
}

func newTSNetServer(dataDir string) *tsnet.Server {
	return &tsnet.Server{
		Dir:      filepath.Join(dataDir, "tsnet"),
		Hostname: *tsnetHost,
		Port:     uint16(*tsnetPort),
	}
}

func prepareCatchTSNetAuth(dataDir string) (string, error) {
	tsAuthKey := strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
	clientSecret := strings.TrimSpace(os.Getenv("TS_CLIENT_SECRET"))
	hasState := catchTSNetStateExists(dataDir)
	if err := validateTSNetInstallAuth(hasState, tsAuthKey, clientSecret); err != nil {
		return "", err
	}
	if clientSecret != "" {
		if hasState {
			if _, err := writeCatchTailscaleClientSecretFn(dataDir, clientSecret); err != nil {
				return "", err
			}
			return tsAuthKey, nil
		}
		tags := catchTailscaleTagsFromEnv(os.Getenv("TS_CATCH_TAGS"))
		authKey, err := generateCatchTailscaleAuthKeyFn(context.Background(), clientSecret, tags)
		if err != nil {
			return "", fmt.Errorf("tailscale OAuth setup failed: %w", err)
		}
		if _, err := writeCatchTailscaleClientSecretFn(dataDir, clientSecret); err != nil {
			return "", err
		}
		return authKey, nil
	}
	return tsAuthKey, nil
}

func validateTSNetInstallAuth(hasState bool, tsAuthKey string, clientSecret string) error {
	if hasState || strings.TrimSpace(tsAuthKey) != "" || strings.TrimSpace(clientSecret) != "" {
		return nil
	}
	return fmt.Errorf("catch Tailscale setup requires a Tailscale OAuth client secret or auth key; run yeet init in a TTY, pass --ts-client-secret=tskey-client-..., or pass --ts-auth-key=<key>; see https://yeetrun.com/docs/concepts/tailscale")
}

func catchTSNetStateExists(dataDir string) bool {
	info, err := os.Stat(catchTSNetStatePath(dataDir))
	return err == nil && !info.IsDir()
}

func catchTSNetStatePath(dataDir string) string {
	return filepath.Join(dataDir, "tsnet", "tailscaled.state")
}

func catchTailscaleTagsFromEnv(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{defaultCatchTag}
	}
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		return []string{defaultCatchTag}
	}
	return tags
}

func tsnetAssignedNameWarning(requested, assignedDNS string) string {
	assigned := shortTailscaleDNSName(assignedDNS)
	requested = strings.TrimSpace(requested)
	if assigned == "" || requested == "" || strings.EqualFold(assigned, requested) {
		return ""
	}
	return fmt.Sprintf("Warning: requested Tailscale hostname %q, but Tailscale assigned %q. Use --host=%s for this catch host, or remove the stale/conflicting Tailscale device and rerun yeet init.", requested, assigned, assigned)
}

func shortTailscaleDNSName(dnsName string) string {
	dnsName = strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	if dnsName == "" {
		return ""
	}
	short, _, _ := strings.Cut(dnsName, ".")
	return short
}

func registerTSNetFallback(ts *tsnet.Server, tsIPs []netip.Addr) {
	ts.RegisterFallbackTCPHandler(func(src, dst netip.AddrPort) (handler func(net.Conn), intercept bool) {
		if !slices.Contains(tsIPs, dst.Addr()) {
			return nil, false
		}

		var d net.Dialer
		dialIP := loopbackForAddr(dst.Addr())
		bc, err := d.Dial("tcp", netip.AddrPortFrom(dialIP, dst.Port()).String())
		if err != nil {
			log.Printf("failed to dial %v: %v", dst, err)
			return nil, false
		}
		return func(cc net.Conn) { proxyConnPair(bc, cc) }, true
	})
}

func loopbackForAddr(addr netip.Addr) netip.Addr {
	if addr.Is4() {
		return ipv4Loopback
	}
	return ipv6Loopback
}

func proxyConnPair(bc, cc net.Conn) {
	defer logClose("backend connection", bc)
	defer logClose("client connection", cc)

	ch := make(chan error, 2)
	go func() {
		_, err := io.Copy(bc, cc)
		ch <- err
	}()
	go func() {
		_, err := io.Copy(cc, bc)
		ch <- err
	}()
	if err := <-ch; err != nil {
		log.Printf("failed to copy: %v", err)
	}
}

func main() {
	flag.Parse()
	if err := runCatchProcess(flag.Args(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func runCatchProcess(args []string, out io.Writer) error {
	if handled, err := handleSpecialCommand(args, out); err != nil {
		return err
	} else if handled {
		return nil
	}
	dataDir := *legacyDataDir
	// Set and create all the necessary directories.
	log.Printf("data dir: %v", dataDir)
	paths := must.Get(prepareDataDirs(dataDir))

	curUser := must.Get(user.Current())
	scfg := newCatchConfig(paths, curUser.Username, *registryInternalAddr, *containerdSocket)
	applyInstallMeta(scfg, dataDir)

	if handled, err := handleLocalCommand(args, scfg, dataDir, out); err != nil {
		return err
	} else if handled {
		return nil
	}

	if err := validateCatchRuntimeFn(*containerdSocket); err != nil {
		return err
	}
	runServer(dataDir, scfg)
	return nil
}

func handleSpecialCommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "is-catch":
		// is-catch is a special command that is used to determine if the
		// binary is a catch binary.
		return true, writeLine(out, "yes")
	case "netns-firewall":
		return true, handleNetNSFirewallCommand(args[1:])
	case "vm-run":
		return true, handleVMRunCommand(args[1:])
	default:
		return false, nil
	}
}

func handleVMRunCommand(args []string) error {
	fs := flag.NewFlagSet("vm-run", flag.ContinueOnError)
	firecracker := fs.String("firecracker", "", "firecracker binary path")
	apiSock := fs.String("api-sock", "", "firecracker API socket path")
	configFile := fs.String("config-file", "", "firecracker config file path")
	consoleSock := fs.String("console-sock", "", "VM serial console socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	err := runVMConsoleProxy(context.Background(), catch.VMConsoleProxyConfig{
		Firecracker:   *firecracker,
		APISocket:     *apiSock,
		ConfigFile:    *configFile,
		ConsoleSocket: *consoleSock,
	})
	if errors.Is(err, catch.ErrVMGuestReboot) {
		exitProcess(catch.VMGuestRebootExitCode)
		return nil
	}
	if errors.Is(err, catch.ErrVMRestoreLoadFailed) {
		exitProcess(catch.VMRestoreLoadFailedExitCode)
		return nil
	}
	return err
}

type catchPaths struct {
	dataDir     string
	dbPath      string
	registryDir string
	servicesDir string
	mountsDir   string
}

func prepareDataDirs(dataDir string) (catchPaths, error) {
	paths := catchPaths{
		dataDir:     dataDir,
		dbPath:      filepath.Join(dataDir, "db.json"),
		registryDir: filepath.Join(dataDir, "registry"),
		servicesDir: filepath.Join(dataDir, "services"),
		mountsDir:   filepath.Join(dataDir, "mounts"),
	}
	for _, dir := range []string{paths.dataDir, paths.registryDir, paths.servicesDir, paths.mountsDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return catchPaths{}, err
		}
	}
	return paths, nil
}

func validateContainerdSocket(socket string) error {
	if socket == "" {
		return fmt.Errorf("containerd socket is required (set --containerd-socket)")
	}
	if _, err := os.Stat(socket); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("containerd socket not found at %s (is containerd running?)", socket)
		}
		return fmt.Errorf("failed to stat containerd socket %s: %w", socket, err)
	}
	return nil
}

func newCatchConfig(paths catchPaths, username, registryAddr, socket string) *catch.Config {
	return &catch.Config{
		DB:                   cdb.NewStore(paths.dbPath, paths.servicesDir),
		DefaultUser:          username, // maybe not default to root?
		InstallUser:          username,
		RootDir:              paths.dataDir,
		ServicesRoot:         paths.servicesDir,
		MountsRoot:           paths.mountsDir,
		InternalRegistryAddr: registryAddr,
		RegistryRoot:         paths.registryDir,
		ContainerdSocket:     socket,
	}
}

func applyInstallMeta(cfg *catch.Config, dataDir string) {
	meta, err := readInstallMeta(dataDir)
	if err != nil {
		return
	}
	if meta.InstallUser != "" {
		cfg.InstallUser = meta.InstallUser
	}
	if meta.InstallHost != "" {
		cfg.InstallHost = meta.InstallHost
	}
}

func handleLocalCommand(args []string, scfg *catch.Config, dataDir string, out io.Writer) (bool, error) {
	if len(args) != 1 {
		return false, nil
	}
	switch args[0] {
	case "version":
		return true, writeLine(out, catch.VersionCommit())
	case "dns":
		return true, runDNSFn(context.Background(), scfg)
	case "install":
		if err := setupDockerFn(); err != nil {
			return true, fmt.Errorf("failed to set up docker: %w", err)
		}
		if err := ensureContainerdSnapshotterForInstallFn(defaultDockerConfigPath); err != nil {
			return true, fmt.Errorf("failed to configure docker: %w", err)
		}
		if err := validateCatchRuntimeFn(*containerdSocket); err != nil {
			return true, fmt.Errorf("failed to validate catch runtime prerequisites: %w", err)
		}
		if err := doInstallFn(scfg, dataDir); err != nil {
			return true, fmt.Errorf("failed to install: %w", err)
		}
		return true, setupVMHostFn()
	default:
		return false, nil
	}
}

func writeLine(out io.Writer, value string) error {
	_, err := fmt.Fprintln(out, value)
	return err
}

func runServer(dataDir string, scfg *catch.Config) {
	// Require tsnet to continue.
	ts, err := initTSNet(dataDir)
	if err != nil {
		log.Fatalf("failed to initialize tsnet: %v", err)
	}
	if ts == nil {
		log.Fatal("failed to initialize tsnet")
	}
	scfg.LocalClient = must.Get(ts.LocalClient())

	// Acquire the listener.
	rpcln := must.Get(ts.Listen("tcp", fmt.Sprintf(":%d", defaultRPCPort)))
	internalRegLn := must.Get(net.Listen("tcp", *registryInternalAddr))
	scfg.InternalRegistryAddr = internalRegLn.Addr().String()
	regln := must.Get(ts.ListenTLS("tcp", ":443"))
	server := catch.NewServer(scfg)
	go startDockerPlugin(scfg.DB)
	go func() {
		if err := http.Serve(internalRegLn, server.RegistryHandler()); err != nil {
			log.Fatalf("internal registry server error: %v", err)
		}
	}()
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/v2/", server.RegistryHandler())
		if err := http.Serve(regln, mux); err != nil {
			log.Fatalf("registry TLS server error: %v", err)
		}
	}()

	// Run the RPC server in the foreground.
	must.Do(http.Serve(rpcln, server.RPCMux()))
}

func validateCatchRuntime(socket string) error {
	if err := validateContainerdSocket(socket); err != nil {
		return err
	}
	return checkContainerdSnapshotterEnabled(defaultDockerConfigPath)
}

func ensureContainerdSnapshotterForInstall(dockerConfigPath string) error {
	changed, err := writeContainerdSnapshotterConfig(dockerConfigPath)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return restartDocker()
}

func writeContainerdSnapshotterConfig(dockerConfigPath string) (bool, error) {
	cfg, err := readDockerConfig(dockerConfigPath)
	if err != nil {
		return false, err
	}
	features, err := dockerConfigFeatures(cfg, dockerConfigPath)
	if err != nil {
		return false, err
	}
	changed := false
	if features["containerd-snapshotter"] != true {
		features["containerd-snapshotter"] = true
		changed = true
	}
	if removeOldDockerDNSDefaults(cfg) {
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := writeDockerConfig(dockerConfigPath, cfg); err != nil {
		return false, err
	}
	return true, nil
}

func removeOldDockerDNSDefaults(cfg map[string]any) bool {
	dns, ok := dockerConfigStringList(cfg["dns"])
	if !ok || !slices.Equal(dns, []string{oldDockerInstallDNS}) {
		return false
	}
	dnsSearch, ok := dockerConfigStringList(cfg["dns-search"])
	if !ok || !slices.Equal(dnsSearch, []string{oldDockerInstallDNSSearch}) {
		return false
	}
	delete(cfg, "dns")
	delete(cfg, "dns-search")
	return true
}

func dockerConfigStringList(raw any) ([]string, bool) {
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func readDockerConfig(dockerConfigPath string) (map[string]any, error) {
	cfg := map[string]any{}
	raw, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read docker config %s: %w", dockerConfigPath, err)
		}
	} else if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse docker config %s: %w", dockerConfigPath, err)
		}
	}
	return cfg, nil
}

func dockerConfigFeatures(cfg map[string]any, dockerConfigPath string) (map[string]any, error) {
	features, ok := cfg["features"].(map[string]any)
	if !ok || features == nil {
		if existing, exists := cfg["features"]; exists && existing != nil {
			return nil, fmt.Errorf("docker config %s has non-object features", dockerConfigPath)
		}
		features = map[string]any{}
		cfg["features"] = features
	}
	return features, nil
}

func writeDockerConfig(dockerConfigPath string, cfg map[string]any) error {
	next, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to render docker config %s: %w", dockerConfigPath, err)
	}
	next = append(next, '\n')
	if err := os.MkdirAll(filepath.Dir(dockerConfigPath), 0o755); err != nil {
		return fmt.Errorf("failed to create docker config dir %s: %w", filepath.Dir(dockerConfigPath), err)
	}
	if err := os.WriteFile(dockerConfigPath, next, 0o644); err != nil {
		return fmt.Errorf("failed to write docker config %s: %w", dockerConfigPath, err)
	}
	return nil
}

func restartDocker() error {
	if err := cmdutil.NewStdCmd("systemctl", "restart", "docker").Run(); err != nil {
		return fmt.Errorf("failed to restart docker: %w", err)
	}
	return nil
}

func checkContainerdSnapshotterEnabled(dockerConfigPath string) error {
	raw, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("docker config %s missing; enable containerd-snapshotter (\"features\": {\"containerd-snapshotter\": true}) and restart docker", dockerConfigPath)
		}
		return fmt.Errorf("failed to read docker config %s: %w", dockerConfigPath, err)
	}
	return verifyContainerdSnapshotterConfig(raw, dockerConfigPath)
}

func verifyContainerdSnapshotterConfig(raw []byte, dockerConfigPath string) error {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("failed to parse docker config %s: %w", dockerConfigPath, err)
	}
	features, ok := cfg["features"].(map[string]any)
	if !ok || features == nil {
		return fmt.Errorf("docker config %s missing features.containerd-snapshotter=true; enable it and restart docker", dockerConfigPath)
	}
	if v, ok := features["containerd-snapshotter"]; !ok || v != true {
		return fmt.Errorf("docker config %s must set features.containerd-snapshotter=true; update and restart docker", dockerConfigPath)
	}
	return nil
}

const dockerInstallScriptURL = "https://get.docker.com"

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type dockerSetupDeps struct {
	dockerCmd  func() (string, error)
	confirm    func(io.Reader, io.Writer, string) (bool, error)
	stdin      io.Reader
	stderr     io.Writer
	getenv     func(string) string
	scriptURL  string
	httpClient httpDoer
	runScript  func(string) error
}

// setupDocker checks if docker is installed and prompts the user to install it.
func setupDocker() error {
	return setupDockerWith(defaultDockerSetupDeps())
}

func defaultDockerSetupDeps() dockerSetupDeps {
	return dockerSetupDeps{
		dockerCmd:  svc.DockerCmd,
		confirm:    cmdutil.Confirm,
		stdin:      os.Stdin,
		stderr:     os.Stderr,
		getenv:     os.Getenv,
		scriptURL:  dockerInstallScriptURL,
		httpClient: http.DefaultClient,
		runScript:  runDockerInstallScript,
	}
}

func setupDockerWith(deps dockerSetupDeps) error {
	deps = normalizeDockerSetupDeps(deps)
	if dockerInstalled(deps.dockerCmd) {
		return nil
	}
	if deps.getenv("CATCH_INSTALL_DOCKER") == "1" {
		if _, err := fmt.Fprintln(deps.stderr, "Installing Docker because CATCH_INSTALL_DOCKER=1"); err != nil {
			return err
		}
		return downloadAndRunDockerInstaller(deps)
	}
	ok, err := confirmDockerInstall(deps.stdin, deps.stderr, deps.confirm)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("docker is required")
	}
	return downloadAndRunDockerInstaller(deps)
}

func normalizeDockerSetupDeps(deps dockerSetupDeps) dockerSetupDeps {
	defaults := defaultDockerSetupDeps()
	if deps.dockerCmd == nil {
		deps.dockerCmd = defaults.dockerCmd
	}
	if deps.confirm == nil {
		deps.confirm = defaults.confirm
	}
	if deps.stdin == nil {
		deps.stdin = defaults.stdin
	}
	if deps.stderr == nil {
		deps.stderr = defaults.stderr
	}
	if deps.getenv == nil {
		deps.getenv = defaults.getenv
	}
	if deps.scriptURL == "" {
		deps.scriptURL = defaults.scriptURL
	}
	if deps.httpClient == nil {
		deps.httpClient = defaults.httpClient
	}
	if deps.runScript == nil {
		deps.runScript = defaults.runScript
	}
	return deps
}

func dockerInstalled(dockerCmd func() (string, error)) bool {
	_, err := dockerCmd()
	return err == nil
}

func confirmDockerInstall(in io.Reader, out io.Writer, confirm func(io.Reader, io.Writer, string) (bool, error)) (bool, error) {
	if _, err := fmt.Fprintln(out, "Warning: docker is required but not installed"); err != nil {
		return false, err
	}
	ok, err := confirm(in, out, "Would you like to install docker?")
	if err != nil {
		return false, fmt.Errorf("failed to confirm: %w", err)
	}
	return ok, nil
}

func downloadAndRunDockerInstaller(deps dockerSetupDeps) error {
	scriptPath, err := createDockerInstallScript(deps)
	if err != nil {
		return err
	}
	defer logRemove(scriptPath)
	return executeDockerInstallScript(deps.runScript, scriptPath)
}

func createDockerInstallScript(deps dockerSetupDeps) (string, error) {
	f, err := os.CreateTemp("", "catch-docker-install")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer logClose("docker install temp file", f)

	if err := downloadDockerInstallScript(deps.httpClient, deps.scriptURL, f); err != nil {
		logRemove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		logRemove(f.Name())
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}
	return f.Name(), nil
}

func downloadDockerInstallScript(client httpDoer, scriptURL string, dst io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", scriptURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create docker install request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download docker install script: %w", err)
	}
	defer logClose("docker install response body", resp.Body)

	if _, err := io.Copy(dst, resp.Body); err != nil {
		return fmt.Errorf("failed to download docker install script: %w", err)
	}
	return nil
}

func executeDockerInstallScript(runScript func(string) error, scriptPath string) error {
	if err := runScript(scriptPath); err != nil {
		return fmt.Errorf("failed to run docker install script: %w", err)
	}
	return nil
}

func runDockerInstallScript(scriptPath string) error {
	return cmdutil.NewStdCmd("sh", scriptPath).Run()
}

type installMeta struct {
	InstallUser string `json:"installUser"`
	InstallHost string `json:"installHost"`
}

func installMetaPath(dataDir string) string {
	return filepath.Join(dataDir, "install.json")
}

func detectInstallUser() string {
	return detectInstallUserFromEnv(os.Getenv, currentUsername)
}

func detectInstallUserFromEnv(getenv func(string) string, current func() (string, error)) string {
	if installUser := getenv("CATCH_INSTALL_USER"); installUser != "" {
		return installUser
	}
	if sudoUser := getenv("SUDO_USER"); sudoUser != "" {
		return sudoUser
	}
	if userEnv := getenv("USER"); userEnv != "" {
		return userEnv
	}
	if cur, err := current(); err == nil && cur != "" {
		return cur
	}
	return ""
}

func currentUsername() (string, error) {
	cur, err := user.Current()
	if err != nil {
		return "", err
	}
	return cur.Username, nil
}

func detectInstallHost() string {
	if installHost := os.Getenv("CATCH_INSTALL_HOST"); installHost != "" {
		return installHost
	}
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return ""
}

func writeInstallMeta(dataDir string) error {
	meta := installMeta{
		InstallUser: detectInstallUser(),
		InstallHost: detectInstallHost(),
	}
	if meta.InstallUser == "" && meta.InstallHost == "" {
		return nil
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(installMetaPath(dataDir), raw, 0600)
}

func readInstallMeta(dataDir string) (installMeta, error) {
	raw, err := os.ReadFile(installMetaPath(dataDir))
	if err != nil {
		return installMeta{}, err
	}
	var meta installMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return installMeta{}, err
	}
	return meta, nil
}

type installTSNet interface {
	Close() error
}

type catchServiceInstaller interface {
	io.Writer
	closeErrorer
	Fail()
}

type catchInstallDeps struct {
	writeInstallMeta func(string) error
	initTSNet        func(string) (installTSNet, error)
	newInstaller     func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error)
	executable       func() (string, error)
	readFile         func(string) ([]byte, error)
	logf             func(string, ...any)
	tsnetHost        func() string
}

type catchInstallPlan struct {
	serviceName string
	dataDir     string
	tsnetHost   string
}

// doInstall installs the catch binary as a service.
func doInstall(cfg *catch.Config, dataDir string) (err error) {
	return doInstallWith(cfg, dataDir, defaultCatchInstallDeps())
}

func defaultCatchInstallDeps() catchInstallDeps {
	return catchInstallDeps{
		writeInstallMeta: writeInstallMeta,
		initTSNet: func(dataDir string) (installTSNet, error) {
			return initTSNet(dataDir)
		},
		newInstaller: newCatchServiceInstaller,
		executable:   os.Executable,
		readFile:     os.ReadFile,
		logf:         log.Printf,
		tsnetHost: func() string {
			return *tsnetHost
		},
	}
}

func doInstallWith(cfg *catch.Config, dataDir string, deps catchInstallDeps) (err error) {
	deps = normalizeCatchInstallDeps(deps)
	if err := validateCatchInstallConfig(cfg); err != nil {
		return err
	}
	recordInstallMetadata(dataDir, deps)

	ts, err := startInstallTSNet(dataDir, deps)
	if err != nil {
		return err
	}
	// Close it at the end so that when the systemd service is started, it
	// doesn't fight for tsnet.
	defer assignOrLogClose(&err, "tsnet server", ts)

	plan := selectCatchInstallMode(dataDir, deps.tsnetHost())
	if err := validateCatchInstallPlan(plan); err != nil {
		return err
	}
	inst, err := deps.newInstaller(cfg, catchFileInstallerConfig(plan))
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	defer assignOrLogClose(&err, "file installer", inst)

	return writeCurrentExecutable(inst, deps)
}

func normalizeCatchInstallDeps(deps catchInstallDeps) catchInstallDeps {
	defaults := defaultCatchInstallDeps()
	if deps.writeInstallMeta == nil {
		deps.writeInstallMeta = defaults.writeInstallMeta
	}
	if deps.initTSNet == nil {
		deps.initTSNet = defaults.initTSNet
	}
	if deps.newInstaller == nil {
		deps.newInstaller = defaults.newInstaller
	}
	if deps.executable == nil {
		deps.executable = defaults.executable
	}
	if deps.readFile == nil {
		deps.readFile = defaults.readFile
	}
	if deps.logf == nil {
		deps.logf = defaults.logf
	}
	if deps.tsnetHost == nil {
		deps.tsnetHost = defaults.tsnetHost
	}
	return deps
}

func validateCatchInstallConfig(cfg *catch.Config) error {
	if cfg == nil {
		return fmt.Errorf("catch config is required")
	}
	return nil
}

func recordInstallMetadata(dataDir string, deps catchInstallDeps) {
	if err := deps.writeInstallMeta(dataDir); err != nil {
		deps.logf("warning: failed to record install metadata: %v", err)
	}
}

func startInstallTSNet(dataDir string, deps catchInstallDeps) (installTSNet, error) {
	ts, err := deps.initTSNet(dataDir)
	if err != nil {
		return nil, err
	}
	if ts == nil {
		return nil, fmt.Errorf("failed to initialize tsnet")
	}
	return ts, nil
}

func selectCatchInstallMode(dataDir, tsnetHost string) catchInstallPlan {
	return catchInstallPlan{
		serviceName: catch.CatchService,
		dataDir:     dataDir,
		tsnetHost:   tsnetHost,
	}
}

func validateCatchInstallPlan(plan catchInstallPlan) error {
	if plan.serviceName == "" {
		return fmt.Errorf("catch install service name is required")
	}
	return nil
}

func catchFileInstallerConfig(plan catchInstallPlan) catch.FileInstallerCfg {
	return catch.FileInstallerCfg{
		InstallerCfg: catch.InstallerCfg{
			ServiceName: plan.serviceName,
			Printer:     log.Printf,
		},
		Args: []string{
			fmt.Sprintf("--data-dir=%v", plan.dataDir),
			fmt.Sprintf("--tsnet-host=%v", plan.tsnetHost),
		},
	}
}

func newCatchServiceInstaller(cfg *catch.Config, installerCfg catch.FileInstallerCfg) (catchServiceInstaller, error) {
	server := catch.NewUnstartedServer(cfg)
	return catch.NewFileInstaller(server, installerCfg)
}

func writeCurrentExecutable(inst catchServiceInstaller, deps catchInstallDeps) error {
	exePath, err := deps.executable()
	if err != nil {
		inst.Fail()
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exe, err := deps.readFile(exePath)
	if err != nil {
		inst.Fail()
		return fmt.Errorf("failed to read executable: %w", err)
	}
	if _, err := inst.Write(exe); err != nil {
		inst.Fail()
		return fmt.Errorf("failed to write executable: %w", err)
	}
	return nil
}

// main function starts the HTTP server
func startDockerPlugin(db *cdb.Store) {
	sock := dockerPluginSocket()
	ln, err := listenDockerPluginSocket(sock)
	if err != nil {
		log.Fatal(err)
	}
	defer logRemove(sock)

	serveDockerPlugin(ln, db)
}

func dockerPluginSocket() string {
	return filepath.Join("/run/docker/plugins", "yeet.sock")
}

func listenDockerPluginSocket(sock string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(sock), 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket dir: %w", err)
	}
	if err := removeStaleSocket(sock); err != nil {
		return nil, err
	}
	fmt.Println("Docker network plugin listening on", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}
	return ln, nil
}

func serveDockerPlugin(ln net.Listener, db *cdb.Store) {
	p := dnet.New(db)
	if err := http.Serve(ln, p); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("docker plugin server error: %v", err)
	}
}

type closeErrorer interface {
	Close() error
}

func logClose(name string, c closeErrorer) {
	if err := c.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		log.Printf("failed to close %s: %v", name, err)
	}
}

func assignOrLogClose(target *error, name string, c closeErrorer) {
	if err := c.Close(); err != nil {
		if *target == nil {
			*target = fmt.Errorf("failed to close %s: %w", name, err)
			return
		}
		log.Printf("failed to close %s: %v", name, err)
	}
}

func logRemove(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to remove %s: %v", path, err)
	}
}

func removeStaleSocket(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket %s: %w", path, err)
	}
	return nil
}
