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
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
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
	"golang.org/x/crypto/ssh"
	"tailscale.com/tsnet"
	"tailscale.com/util/must"
)

const (
	defaultTSNetPort  = 41547 // above CATCH on QWERTY
	defaultControlURL = "https://sky.yeet.net"
)

var (
	legacyDataDir = flag.String("data-dir", must.Get(filepath.Abs("data")), "data directory")
	tsnetHost     = flag.String("tsnet-host", "catch", "hostname to use for tsnet")
	tsnetPort     = flag.Int("tsnet-port", defaultTSNetPort, "port to use for tsnet")

	// TODO: This should be randomly assigned at stored in the JSON DB.
	registryInternalAddr = flag.String("registry-internal-addr", "127.0.0.1:0", "address for registry to listen on internally")
)

var (
	ipv4Loopback = netip.MustParseAddr("127.0.0.1")
	ipv6Loopback = netip.MustParseAddr("::1")
)

// initTSNet initializes and returns a tsnet.Server if tsnetHost is set.
func initTSNet(dataDir string) *tsnet.Server {
	controlURL := os.Getenv("YEET_CONTROL_URL")
	if controlURL == "" {
		controlURL = defaultControlURL
	}
	if *tsnetHost == "" {
		return nil
	}
	ts := &tsnet.Server{
		Dir:        filepath.Join(dataDir, "tsnet"),
		Hostname:   *tsnetHost,
		Port:       uint16(*tsnetPort),
		ControlURL: controlURL,
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

	// Load or generate private key.
	kp := filepath.Join(dataDir, "id_ed25519")
	privateBytes, err := os.ReadFile(kp) // TODO create one per data dir
	if err != nil {
		if !os.IsNotExist(err) {
			log.Fatal("failed to load private key: ", err)
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal("failed to generate private key: ", err)
		}
		mk := must.Get(x509.MarshalPKCS8PrivateKey(priv))
		privateBytes = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mk})
		must.Do(os.WriteFile(kp, privateBytes, 0600))
	}
	private := must.Get(ssh.ParsePrivateKey(privateBytes))

	curUser := must.Get(user.Current())
	irAddr := *registryInternalAddr
	scfg := &catch.Config{
		Signer:               private,
		DB:                   cdb.NewStore(dbPath, servicesDir),
		DefaultUser:          curUser.Username, // maybe not default to root?
		RootDir:              dataDir,
		ServicesRoot:         servicesDir,
		MountsRoot:           mountsDir,
		InternalRegistryAddr: irAddr,
		RegistryRoot:         registryDir,
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

	// domains := ts.CertDomains()
	// if len(domains) == 0 {
	// 	log.Fatal("no Tailscale cert domains available; is HTTPS enabled on the tailnet?")
	// }
	// scfg.ExternalRegistryAddr = domains[0]
	// log.Printf("Registry listening on https://%v", scfg.ExternalRegistryAddr)
	// go func() {
	// 	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	// 	defer cancel()
	// 	_, _, err := scfg.LocalClient.CertPair(ctx, domains[0])
	// 	if err != nil {
	// 		log.Fatalf("failed to get cert pair for %v: %v", domains[0], err)
	// 	}
	// 	log.Printf("Successfully got cert pair for %v", domains[0])
	// }()

	// Acquire the listeners.
	sshln := must.Get(ts.Listen("tcp", ":22"))
	// internalRegLn := must.Get(net.Listen("tcp", *registryInternalAddr))
	// scfg.InternalRegistryAddr = internalRegLn.Addr().String()
	server := catch.NewServer(scfg)
	// go func() {
	// 	ln := must.Get(ts.Listen("tcp", ":80"))
	// 	hs := &http.Server{
	// 		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	// 			// Redirect to https://domains[0]
	// 			http.Redirect(w, r, "https://"+domains[0]+r.URL.Path, http.StatusTemporaryRedirect)
	// 		}),
	// 	}
	// 	must.Do(hs.Serve(ln))
	// }()
	// go func() {
	// 	webLn := must.Get(ts.Listen("tcp", ":443"))
	// 	hs := &http.Server{
	// 		Handler: must.Get(server.WebMux()),
	// 		TLSConfig: &tls.Config{
	// 			GetCertificate: scfg.LocalClient.GetCertificate,
	// 		},
	// 	}
	// 	must.Do(hs.ServeTLS(webLn, "", ""))
	// }()
	// go func() {
	// 	must.Do(server.ServeInternalRegistry(internalRegLn))
	// }()
	go startDockerPlugin(scfg.DB)

	// Run the SSH server in the foreground.
	must.Do(server.ServeSSH(sshln))
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
