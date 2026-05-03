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
	"time"

	"github.com/yeetrun/yeet/pkg/catch"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	cdb "github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/dnet"
	"github.com/yeetrun/yeet/pkg/svc"
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

// initTSNet initializes and returns a tsnet.Server if tsnetHost is set.
func initTSNet(dataDir string) *tsnet.Server {
	if *tsnetHost == "" {
		return nil
	}
	ts := newTSNetServer(dataDir)
	st := must.Get(ts.Up(context.Background()))
	registerTSNetFallback(ts, st.TailscaleIPs)
	return ts
}

func newTSNetServer(dataDir string) *tsnet.Server {
	return &tsnet.Server{
		Dir:      filepath.Join(dataDir, "tsnet"),
		Hostname: *tsnetHost,
		Port:     uint16(*tsnetPort),
	}
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
	if handled, err := handleSpecialCommand(flag.Args(), os.Stdout); err != nil {
		log.Fatal(err)
	} else if handled {
		return
	}

	dataDir := *legacyDataDir
	// Set and create all the necessary directories.
	log.Printf("data dir: %v", dataDir)
	paths := must.Get(prepareDataDirs(dataDir))

	curUser := must.Get(user.Current())
	must.Do(validateContainerdSocket(*containerdSocket))
	ensureContainerdSnapshotterEnabled()
	scfg := newCatchConfig(paths, curUser.Username, *registryInternalAddr, *containerdSocket)
	applyInstallMeta(scfg, dataDir)

	if handled, err := handleLocalCommand(flag.Args(), scfg, dataDir, os.Stdout); err != nil {
		log.Fatal(err)
	} else if handled {
		return
	}

	runServer(dataDir, scfg)
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
	default:
		return false, nil
	}
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
	case "install":
		if err := doInstall(scfg, dataDir); err != nil {
			return true, fmt.Errorf("failed to install: %w", err)
		}
		return true, setupDocker()
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
	ts := initTSNet(dataDir)
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

func ensureContainerdSnapshotterEnabled() {
	const dockerConfigPath = "/etc/docker/daemon.json"
	if err := checkContainerdSnapshotterEnabled(dockerConfigPath); err != nil {
		log.Fatal(err)
	}
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

// setupDocker checks if docker is installed and prompts the user to install it.
func setupDocker() error {
	if _, err := svc.DockerCmd(); err == nil {
		// Docker is installed
		return nil
	}
	fmt.Fprintln(os.Stderr, "Warning: docker is recommended but not installed")
	ok, err := cmdutil.Confirm(os.Stdin, os.Stderr, "Would you like to install docker?")
	if err != nil {
		return fmt.Errorf("failed to confirm: %w", err)
	}
	if !ok {
		return nil
	}
	f, err := os.CreateTemp("", "catch-docker-install")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer logRemove(f.Name())
	defer logClose("docker install temp file", f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := must.Get(http.NewRequestWithContext(ctx, "GET", "https://get.docker.com", nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download docker install script: %w", err)
	}
	defer logClose("docker install response body", resp.Body)

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("failed to download docker install script: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := cmdutil.NewStdCmd("sh", f.Name()).Run(); err != nil {
		return fmt.Errorf("failed to run docker install script: %w", err)
	}
	return nil
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

// doInstall installs the catch binary as a service.
func doInstall(cfg *catch.Config, dataDir string) (err error) {
	if err := writeInstallMeta(dataDir); err != nil {
		log.Printf("warning: failed to record install metadata: %v", err)
	}
	// Set up Tailscale
	ts := initTSNet(dataDir)
	if ts == nil {
		return fmt.Errorf("failed to initialize tsnet")
	}
	// Close it at the end so that when the systedm service is started, it
	// doesn't fight for tsnet.
	defer assignOrLogClose(&err, "tsnet server", ts)

	server := catch.NewUnstartedServer(cfg)
	inst, err := catch.NewFileInstaller(server, catch.FileInstallerCfg{
		InstallerCfg: catch.InstallerCfg{
			ServiceName: catch.CatchService,
			Printer:     log.Printf,
		},
		Args: []string{
			fmt.Sprintf("--data-dir=%v", dataDir),
			fmt.Sprintf("--tsnet-host=%v", *tsnetHost),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	defer assignOrLogClose(&err, "file installer", inst)

	exePath, err := os.Executable()
	if err != nil {
		inst.Fail()
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exe, err := os.ReadFile(exePath)
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
