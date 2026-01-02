// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
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
	rpcPort       = flag.Int("rpc-port", defaultRPCPort, "port to use for RPC server")

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
	ts := &tsnet.Server{
		Dir:        filepath.Join(dataDir, "tsnet"),
		Hostname:   *tsnetHost,
		Port:       uint16(*tsnetPort),
	}
	st := must.Get(ts.Up(context.Background()))
	tsIPs := st.TailscaleIPs

	ts.RegisterFallbackTCPHandler(func(src, dst netip.AddrPort) (handler func(net.Conn), intercept bool) {
		var d net.Dialer
		if !slices.Contains(tsIPs, dst.Addr()) {
			return nil, false
		}

		var dialIP netip.Addr
		if dst.Addr().Is4() {
			dialIP = ipv4Loopback
		} else {
			dialIP = ipv6Loopback
		}

		bc, err := d.Dial("tcp", netip.AddrPortFrom(dialIP, dst.Port()).String())
		if err != nil {
			log.Printf("failed to dial %v: %v", dst, err)
			return nil, false
		}
		return func(cc net.Conn) {
			defer bc.Close()
			defer cc.Close()
			ch := make(chan error, 1)
			go func() {
				_, err = io.Copy(bc, cc)
				ch <- err
			}()
			go func() {
				_, err = io.Copy(cc, bc)
				ch <- err
			}()
			if err := <-ch; err != nil {
				log.Printf("failed to copy: %v", err)
			}
		}, true
	})
	return ts
}

func main() {
	flag.Parse()
	// Fast path for is-catch command.
	if len(flag.Args()) > 0 && flag.Args()[0] == "is-catch" {
		// is-catch is a special command that is used to determine if the
		// binary is a catch binary.
		fmt.Println("yes")
		return
	}

	dataDir := *legacyDataDir
	// Set and create all the necessary directories.
	log.Printf("data dir: %v", dataDir)
	dbPath := filepath.Join(dataDir, "db.json")

	must.Do(os.MkdirAll(dataDir, 0700))
	registryDir := filepath.Join(dataDir, "registry")
	must.Do(os.MkdirAll(registryDir, 0700))
	servicesDir := filepath.Join(dataDir, "services")
	must.Do(os.MkdirAll(servicesDir, 0700))
	mountsDir := filepath.Join(dataDir, "mounts")
	must.Do(os.MkdirAll(mountsDir, 0700))

	curUser := must.Get(user.Current())
	if *containerdSocket == "" {
		log.Fatal("containerd socket is required (set --containerd-socket)")
	}
	if _, err := os.Stat(*containerdSocket); err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("containerd socket not found at %s (is containerd running?)", *containerdSocket)
		}
		log.Fatalf("failed to stat containerd socket %s: %v", *containerdSocket, err)
	}
	ensureContainerdSnapshotterEnabled()
	irAddr := *registryInternalAddr
	scfg := &catch.Config{
		DB:                   cdb.NewStore(dbPath, servicesDir),
		DefaultUser:          curUser.Username, // maybe not default to root?
		RootDir:              dataDir,
		ServicesRoot:         servicesDir,
		MountsRoot:           mountsDir,
		InternalRegistryAddr: irAddr,
		RegistryRoot:         registryDir,
		ContainerdSocket:     *containerdSocket,
	}

	if len(flag.Args()) == 1 {
		cmd := flag.Arg(0)
		switch cmd {
		case "version":
			fmt.Println(catch.VersionCommit())
			return
		case "install":
			// Perform install
			if err := doInstall(scfg, dataDir); err != nil {
				log.Fatal("failed to install: ", err)
			}
			setupDocker()
			return
		}
	}

	// Require tsnet to continue.
	ts := initTSNet(dataDir)
	if ts == nil {
		log.Fatal("failed to initialize tsnet")
	}
	scfg.LocalClient = must.Get(ts.LocalClient())

	// Acquire the listener.
	rpcln := must.Get(ts.Listen("tcp", fmt.Sprintf(":%d", *rpcPort)))
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
	raw, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("docker config %s missing; enable containerd-snapshotter (\"features\": {\"containerd-snapshotter\": true}) and restart docker", dockerConfigPath)
		}
		log.Fatalf("failed to read docker config %s: %v", dockerConfigPath, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("failed to parse docker config %s: %v", dockerConfigPath, err)
	}
	features, ok := cfg["features"].(map[string]any)
	if !ok || features == nil {
		log.Fatalf("docker config %s missing features.containerd-snapshotter=true; enable it and restart docker", dockerConfigPath)
	}
	if v, ok := features["containerd-snapshotter"]; !ok || v != true {
		log.Fatalf("docker config %s must set features.containerd-snapshotter=true; update and restart docker", dockerConfigPath)
	}
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
		log.Fatal("failed to confirm: ", err)
	}
	if !ok {
		return nil
	}
	f, err := os.CreateTemp("", "catch-docker-install")
	if err != nil {
		log.Fatal("failed to create temp file: ", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := must.Get(http.NewRequestWithContext(ctx, "GET", "https://get.docker.com", nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal("failed to download docker install script: ", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		log.Fatal("failed to download docker install script: ", err)
	}
	if err := f.Close(); err != nil {
		log.Fatal("failed to close temp file: ", err)
	}

	if err := cmdutil.NewStdCmd("sh", f.Name()).Run(); err != nil {
		log.Fatal("failed to run docker install script: ", err)
	}
	return nil
}

// doInstall installs the catch binary as a service.
func doInstall(cfg *catch.Config, dataDir string) error {
	// Set up Tailscale
	ts := initTSNet(dataDir)
	// Close it at the end so that when the systedm service is started, it
	// doesn't fight for tsnet.
	defer ts.Close()
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
	defer inst.Close()

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
	sock := filepath.Join("/run/docker/plugins", "yeet.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0755); err != nil {
		log.Fatalf("failed to create socket dir: %v", err)
	}
	os.Remove(sock)
	fmt.Println("Docker network plugin listening on", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Fatalf("failed to listen on socket: %v", err)
	}
	defer os.Remove(sock)
	p := dnet.New(db)
	http.Serve(ln, p)
}
