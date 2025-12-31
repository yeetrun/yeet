// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/net/netmon"
	"tailscale.com/tstime/rate"
	"tailscale.com/types/lazy"
	"tailscale.com/util/mak"
	"tailscale.com/util/set"
)

type FileInstallerCfg struct {
	InstallerCfg
	EnvFile bool

	Args      []string
	Network   NetworkOpts
	StageOnly bool
	NoBinary  bool
	// PayloadName preserves the original filename for type detection.
	PayloadName string

	// NewCmd, if set, will be used to create a new exec.Cmd.
	NewCmd func(name string, arg ...string) *exec.Cmd
}

type TailscaleOpts struct {
	Version  string
	ExitNode string
	Tags     []string
	AuthKey  string
}

type MacvlanOpts struct {
	Mac    string
	Parent string
	VLAN   int
}

type NetworkOpts struct {
	Interfaces string
	Tailscale  TailscaleOpts
	Macvlan    MacvlanOpts
}

type FileInstaller struct {
	s   *Server
	cfg FileInstallerCfg
	ch  chan struct{}

	existingService db.ServiceView
	svcNet          *db.SvcNetwork
	macvlan         *db.MacvlanNetwork
	tsNet           *db.TailscaleNetwork
	tsAuthKey       string
	artifacts       map[db.ArtifactName]string
	lazyNetwork     lazy.GValue[*networkConfig]

	File     *os.File
	received atomic.Int64
	rateVal  rate.Value

	err    error
	closed bool

	ver string // memoized version number

	failed bool

	tmpDir  string
	tmpPath string
}

func (i *FileInstaller) WriteAt(p []byte, offset int64) (n int, err error) {
	if i.File == nil {
		return 0, fmt.Errorf("no temporary file")
	}
	i.received.Add(int64(len(p)))
	i.rateVal.Add(float64(len(p)))
	return i.File.WriteAt(p, offset)
}

func (i *FileInstaller) Write(p []byte) (n int, err error) {
	if i.File == nil {
		return 0, fmt.Errorf("no temporary file")
	}
	i.received.Add(int64(len(p)))
	i.rateVal.Add(float64(len(p)))
	return i.File.Write(p)
}

func (i *FileInstaller) Wait() error {
	<-i.ch
	return nil
}

func (i *FileInstaller) Received() float64 {
	return float64(i.received.Load())
}

func (i *FileInstaller) Rate() float64 {
	return i.rateVal.Rate()
}

func First[T1, T2 any](t1 T1, _ T2) T1 {
	return t1
}

var reservedServiceNames = map[string]struct{}{
	string(db.ArtifactTSBinary): {},
}

func NewFileInstaller(s *Server, cfg FileInstallerCfg) (*FileInstaller, error) {
	if _, ok := reservedServiceNames[cfg.ServiceName]; ok {
		return nil, fmt.Errorf("%s is a reserved service name", cfg.ServiceName)
	}
	i := &FileInstaller{
		s:   s,
		cfg: cfg,
		ch:  make(chan struct{}),
		rateVal: rate.Value{
			HalfLife: 250 * time.Millisecond,
		},
		existingService: First(s.serviceView(cfg.ServiceName)),
	}
	if i.cfg.NewCmd == nil {
		i.cfg.NewCmd = cmdutil.NewStdCmd
	}
	if err := s.ensureDirs(cfg.ServiceName, cfg.User); err != nil {
		return nil, fmt.Errorf("failed to ensure directories: %w", err)
	}
	if err := i.initTempFile(); err != nil {
		return nil, err
	}
	// Create temporary file.
	var err error
	i.File, err = os.OpenFile(i.tempFilePath(), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		i.cleanupTemp()
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	return i, nil
}

func (i *FileInstaller) printf(format string, args ...interface{}) {
	if i.cfg.Printer != nil {
		i.cfg.Printer(format, args...)
	}
}

type networkConfig struct {
	NetNS string
	Deps  []string
}

func hexStr(n int) string {
	bytes := make([]byte, n)
	for i := range bytes {
		bytes[i] = byte(rand.N(256))
	}
	return hex.EncodeToString(bytes)
}

func (i *FileInstaller) parseNetwork() error {
	nets := strings.Split(i.cfg.Network.Interfaces, ",")
	if len(nets) == 0 {
		return fmt.Errorf("invalid network: %q", i.cfg.Network.Interfaces)
	}
	dv, err := i.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get db view: %w", err)
	}
	for _, net := range nets {
		switch {
		case net == "ts":
			i.tsNet = &db.TailscaleNetwork{
				Interface: "yts-" + hexStr(4),
				Version:   "1.77.33",
			}
			if i.cfg.Network.Tailscale.Version != "" {
				i.tsNet.Version = i.cfg.Network.Tailscale.Version
			}
			if i.cfg.Network.Tailscale.Tags != nil {
				i.tsNet.Tags = i.cfg.Network.Tailscale.Tags
			}
			if i.cfg.Network.Tailscale.ExitNode != "" {
				i.tsNet.ExitNode = i.cfg.Network.Tailscale.ExitNode
			}
			i.tsAuthKey = i.cfg.Network.Tailscale.AuthKey
		case net == "svc":
			ip, err := unassignedIP(dv)
			if err != nil {
				return fmt.Errorf("failed to get unassigned IP: %v", err)
			}
			i.svcNet = &db.SvcNetwork{
				IPv4: ip,
			}
		case net == "lan":
			iface, err := netmon.DefaultRouteInterface()
			if err != nil {
				return fmt.Errorf("failed to get default route interface: %v", err)
			}
			log.Printf("default route interface: %v", iface)
			i.macvlan = &db.MacvlanNetwork{
				Interface: "ymv-" + hexStr(4),
				Parent:    iface,
				Mac:       randomMAC(),
			}
			if i.cfg.Network.Macvlan.Parent != "" {
				i.macvlan.Parent = i.cfg.Network.Macvlan.Parent
			}
			if i.cfg.Network.Macvlan.VLAN != 0 {
				i.macvlan.VLAN = i.cfg.Network.Macvlan.VLAN
			}
			if i.cfg.Network.Macvlan.Mac != "" {
				i.macvlan.Mac = i.cfg.Network.Macvlan.Mac
			}
		default:
			return fmt.Errorf("unknown network: %q", net)
		}
	}
	return nil
}

const tailscaledResolvConf = `nameserver 100.100.100.100` + "\n"

func (i *FileInstaller) configureNetwork() (*networkConfig, error) {
	return i.lazyNetwork.GetErr(func() (*networkConfig, error) {
		if i.cfg.Network.Interfaces == "host" || i.cfg.Network.Interfaces == "" {
			return nil, nil
		}
		if err := i.parseNetwork(); err != nil {
			return nil, fmt.Errorf("failed to parse network: %v", err)
		}
		env := netns.Service{
			ServiceName: i.cfg.ServiceName,
		}
		if i.svcNet != nil {
			env.ServiceIP = netip.PrefixFrom(i.svcNet.IPv4, i.svcNet.IPv4.BitLen())
			env.Range = netip.MustParsePrefix("192.168.100.0/24")
			env.HostIP = netip.MustParseAddr("192.168.100.1")
			env.YeetIP = netip.MustParseAddr("192.168.100.254")
		}
		if i.macvlan != nil {
			env.MacvlanParent = i.macvlan.Parent
			env.MacvlanMac = i.macvlan.Mac
			env.MacvlanInterface = i.macvlan.Interface
			if i.macvlan.VLAN != 0 {
				env.MacvlanVLAN = strconv.Itoa(i.macvlan.VLAN)
			}
		}
		var runTSInNetNS string
		var netnsResolvConf string
		tsTapMode := i.tsNet != nil && i.svcNet == nil && i.macvlan == nil
		if i.tsNet != nil {
			if tsTapMode {
				env.TailscaleTAPInterface = i.tsNet.Interface
				netnsResolvConf = tailscaledResolvConf
			} else {
				runTSInNetNS = env.NetNS()
			}
		}

		if netnsResolvConf == "" {
			// Just pick one of the public DNS servers.
			// TODO: make it a flag.
			const defaultNameserver = "8.8.8.8"
			dns := defaultNameserver
			if v := os.Getenv("DEFAULT_NS"); v != "" {
				dns = v
			}
			var searchDomains string
			if v := os.Getenv("DEFAULT_SEARCH_DOMAINS"); v != "" {
				searchDomains = v
			}
			netnsResolvConf = fmt.Sprintf("nameserver %s\n", dns)
			if searchDomains != "" {
				netnsResolvConf += fmt.Sprintf("search %s\n", searchDomains)
			}
		}

		binDir := i.s.serviceBinDir(i.cfg.ServiceName)
		runDir := i.s.serviceRunDir(i.cfg.ServiceName)
		if netnsResolvConf != "" {
			fp := filepath.Join(binDir, fileutil.ApplyVersion("resolv.conf"))
			if err := os.WriteFile(fp, []byte(netnsResolvConf), 0644); err != nil {
				return nil, fmt.Errorf("failed to write resolv.conf: %v", err)
			}
			mak.Set(&i.artifacts, db.ArtifactNetNSResolv, fp)
			env.ResolvConf = fp
		}
		files, err := netns.WriteServiceNetNS(binDir, runDir, env)
		if err != nil {
			return nil, fmt.Errorf("failed to write netns: %v", err)
		}
		for k, v := range files {
			mak.Set(&i.artifacts, k, v)
		}
		deps := []string{
			env.ServiceUnit(),
		}
		if i.tsNet != nil {
			rc := "/etc/netns/" + env.NetNS() + "/resolv.conf"
			if tsTapMode {
				// Tailscale in TAP mode runs in the host namespace, so we don't need
				// a resolv.conf file.
				rc = ""
			}
			files, err := i.s.installTS(i.cfg.ServiceName, runTSInNetNS, i.tsNet, i.tsAuthKey, rc)
			if err != nil {
				return nil, fmt.Errorf("failed to install tailscale: %v", err)
			}
			for k, v := range files {
				mak.Set(&i.artifacts, k, v)
			}
			deps = append(deps, "yeet-"+i.cfg.ServiceName+"-ts.service")
		}
		dockerNet := fmt.Sprintf(`networks:
  default:
    driver: yeet
    driver_opts:
      dev.catchit.netns: %q
`, filepath.Join("/var/run/netns", env.NetNS()))
		dnf := filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), "compose.network")
		if err := os.WriteFile(dnf, []byte(dockerNet), 0644); err != nil {
			return nil, fmt.Errorf("failed to write docker compose network: %v", err)
		}
		mak.Set(&i.artifacts, db.ArtifactDockerComposeNetwork, dnf)
		log.Printf("artifacts: %v", i.artifacts)
		return &networkConfig{
			NetNS: env.NetNS(),
			Deps:  deps,
		}, nil
	})
}

// Close closes the temporary file and installs the service.
func (i *FileInstaller) Close() (err error) {
	if i.err != nil {
		return i.err
	}
	if i.closed {
		return nil
	}
	if i.File == nil {
		return fmt.Errorf("no temporary file")
	}

	defer func() {
		i.cleanupTemp()
		i.File = nil
		i.closed = true
		close(i.ch)
		i.err = err
	}()
	if err := i.File.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %v", err)
	}
	if i.failed {
		log.Printf("Installation of %q failed\n", i.cfg.ServiceName)
		i.printf("Installation of %q failed\n", i.cfg.ServiceName)
		return fmt.Errorf("installation failed")
	}
	if err := i.installOnClose(); err != nil {
		log.Printf("Failed to install service: %v", err)
		i.printf("Failed to install service: %v", err)
		return fmt.Errorf("failed to install service: %w", err)
	}
	return nil
}

func rewriteSystemdUnit(p, exe string, args []string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("failed to open systemd unit: %w", err)
	}
	defer f.Close()
	out, err := os.Create(fileutil.UpdateVersion(p))
	if err != nil {
		return "", fmt.Errorf("failed to create systemd unit: %w", err)
	}
	defer out.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "ExecStart=") {
			fmt.Fprintf(out, "ExecStart=%s %s\n", exe, strings.Join(args, " "))
		} else {
			fmt.Fprintln(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("failed to read systemd unit: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("failed to close systemd unit: %w", err)
	}
	return out.Name(), nil
}

func (i *FileInstaller) ensureSystemdUnit() error {
	runDir := i.s.serviceRunDir(i.cfg.ServiceName)
	exe := filepath.Join(runDir, i.cfg.ServiceName)
	if i.existingService.Valid() {
		s := i.existingService.AsStruct()
		p, ok := s.Artifacts.Staged(db.ArtifactSystemdUnit)
		if ok {
			if i.cfg.Args != nil {
				p, err := rewriteSystemdUnit(p, exe, i.cfg.Args)
				if err != nil {
					return fmt.Errorf("failed to rewrite systemd unit: %w", err)
				}
				mak.Set(&i.artifacts, db.ArtifactSystemdUnit, p)
			}
			return nil
		}
	}
	if i.cfg.StageOnly && i.cfg.Network.Interfaces == "" && i.cfg.Args == nil {
		return nil
	}
	// If the service is not valid, we need to create a systemd unit file
	// that will start the binary.
	su := &svc.SystemdUnit{
		Name:             i.cfg.ServiceName,
		Executable:       exe,
		WorkingDirectory: i.s.serviceDataDir(i.cfg.ServiceName),
		Arguments:        i.cfg.Args,
		EnvFile:          "-" + filepath.Join(runDir, "env"), // "-" means optional
		Timer:            i.cfg.Timer,
	}

	if n, err := i.configureNetwork(); err != nil {
		return fmt.Errorf("failed to configure network: %v", err)
	} else if n != nil {
		su.NetNS = n.NetNS
		su.Requires = strings.Join(n.Deps, " ")
		su.ResolvConf = fmt.Sprintf("/etc/netns/%s/resolv.conf", su.NetNS)
	}
	log.Printf("NetNS: %v", su.NetNS)
	log.Printf("Requires: %v", su.Requires)
	units, err := su.WriteOutUnitFiles(i.s.serviceBinDir(i.cfg.ServiceName))
	if err != nil {
		return fmt.Errorf("failed to write unit files: %v", err)
	}
	for u, p := range units {
		mak.Set(&i.artifacts, u, p)
	}
	return nil
}

func (i *FileInstaller) installOnClose() error {
	if i.File == nil {
		return fmt.Errorf("no temporary file")
	}
	tmppath := i.tempFilePath()

	bin := tmppath
	var dst string
	var postRenameActions []func() error
	var detectedServiceType db.ServiceType
	if i.cfg.EnvFile {
		er := i.s.serviceEnvDir(i.cfg.ServiceName)
		dst = filepath.Join(er, "env-"+i.version())
		mak.Set(&i.artifacts, db.ArtifactEnvFile, dst)
	} else if i.cfg.NoBinary {
		if i.existingService.Valid() {
			detectedServiceType = i.existingService.ServiceType()
			if detectedServiceType == db.ServiceTypeSystemd {
				if err := i.ensureSystemdUnit(); err != nil {
					return fmt.Errorf("failed to ensure systemd unit: %w", err)
				}
			}
		}
	} else {
		// Detect file type.
		var err error
		binFT, err := ftdetect.DetectFile(bin, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return fmt.Errorf("failed to detect file type: %w", err)
		}
		if binFT == ftdetect.Zstd {
			// Unpack zstd compressed files.
			unpackPath := tmppath + ".unpack"
			defer os.Remove(unpackPath)
			if err := codecutil.ZstdDecompress(bin, unpackPath); err != nil {
				return fmt.Errorf("failed to decompress file: %w", err)
			}
			// Replace the original file with the unpacked file.
			if err := os.Rename(unpackPath, bin); err != nil {
				return fmt.Errorf("failed to rename file: %w", err)
			}
			binFT, err = ftdetect.DetectFile(bin, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return fmt.Errorf("failed to detect file type: %w", err)
			}
		}

		if i.cfg.Pull && binFT != ftdetect.DockerCompose {
			return fmt.Errorf("--pull is only valid for docker compose payloads")
		}

		var artifactName db.ArtifactName
		// Set the service type and "binary" name (file in the bin/ dir)
		switch binFT {
		case ftdetect.Binary, ftdetect.Script:
			if binFT == ftdetect.Script {
				i.printf("Detected script file\n")
			} else {
				i.printf("Detected binary file\n")
			}
			// serviceType = db.ServiceTypeSystemd
			binName := fmt.Sprintf("%s-%s", i.cfg.ServiceName, i.version())
			// Move the "binary" file to the final location.
			dst = filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), binName)
			postRenameActions = append(postRenameActions, func() error {
				if err := os.Chmod(dst, 0755); err != nil {
					return fmt.Errorf("failed to make binary executable: %w", err)
				}
				return nil
			})
			artifactName = db.ArtifactBinary
			detectedServiceType = db.ServiceTypeSystemd
			if err := i.ensureSystemdUnit(); err != nil {
				return fmt.Errorf("failed to ensure systemd unit: %w", err)
			}
		case ftdetect.DockerCompose:
			i.printf("Detected Docker Compose file\n")
			// serviceType = db.ServiceTypeDockerCompose
			binName := fmt.Sprintf("docker-compose.%s.yml", i.version())
			// Move the "binary" file to the final location.
			dst = filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), binName)
			artifactName = db.ArtifactDockerComposeFile
			detectedServiceType = db.ServiceTypeDockerCompose
		case ftdetect.TypeScript:
			i.printf("Detected TypeScript file\n")
			// TypeScript runs in a Docker container but is installed as a systemd
			// service. I know, it's weird.
			// serviceType = db.ServiceTypeSystemd
			binName := fmt.Sprintf("main.%s.ts", i.version())
			// Move the "binary" file to the final location.
			binDir := i.s.serviceBinDir(i.cfg.ServiceName)
			runDir := i.s.serviceRunDir(i.cfg.ServiceName)
			dataDir := i.s.serviceDataDir(i.cfg.ServiceName)
			dst = filepath.Join(binDir, binName)
			dockerCmd, err := svc.DockerCmd()
			if err != nil {
				return fmt.Errorf("failed get Docker cmd: %w", err)
			}
			su := &svc.SystemdUnit{
				Name:             i.cfg.ServiceName,
				Executable:       dockerCmd,
				WorkingDirectory: dataDir,
				Arguments: append([]string{
					"run", "--rm", "--tty",
					"--net", "host",
					"--volume", fmt.Sprintf("%s:/data", dataDir),
					"--volume", fmt.Sprintf("%s:/main.ts", filepath.Join(runDir, "main.ts")),
					"denoland/deno:2.0.0-rc.2",
					"run", "--allow-net",
					"/main.ts",
				}, i.cfg.Args...),
			}
			units, err := su.WriteOutUnitFiles(binDir)
			if err != nil {
				return fmt.Errorf("failed to write unit files: %v", err)
			}
			for u, p := range units {
				mak.Set(&i.artifacts, u, p)
			}
			// TODO: add support for user deno flags
			artifactName = db.ArtifactTypeScriptFile
			detectedServiceType = db.ServiceTypeSystemd
		case ftdetect.Python:
			i.printf("Detected Python file\n")
			// Python runs in a Docker container but is installed as a systemd
			// service, similar to TypeScript
			binName := fmt.Sprintf("main.%s.py", i.version())
			// Move the "binary" file to the final location.
			binDir := i.s.serviceBinDir(i.cfg.ServiceName)
			runDir := i.s.serviceRunDir(i.cfg.ServiceName)
			dataDir := i.s.serviceDataDir(i.cfg.ServiceName)
			dst = filepath.Join(binDir, binName)
			dockerCmd, err := svc.DockerCmd()
			if err != nil {
				return fmt.Errorf("failed get Docker cmd: %w", err)
			}

			uvArgs := []string{"uv", "run", "/main.py"}

			su := &svc.SystemdUnit{
				Name:             i.cfg.ServiceName,
				Executable:       dockerCmd,
				WorkingDirectory: dataDir,
				Arguments: append([]string{
					"run", "--rm", "--tty",
					"--net", "host",
					"--volume", fmt.Sprintf("%s:/data", dataDir),
					"--volume", fmt.Sprintf("%s:/main.py", filepath.Join(runDir, "main.py")),
					"ghcr.io/astral-sh/uv:python3.13-bookworm-slim",
				}, append(uvArgs, i.cfg.Args...)...),
			}
			units, err := su.WriteOutUnitFiles(binDir)
			if err != nil {
				return fmt.Errorf("failed to write unit files: %v", err)
			}
			for u, p := range units {
				mak.Set(&i.artifacts, u, p)
			}
			artifactName = db.ArtifactPythonFile
			detectedServiceType = db.ServiceTypeSystemd
		case ftdetect.Unknown:
			return fmt.Errorf("unknown file type")
		}
		mak.Set(&i.artifacts, artifactName, dst)
	}

	if dst != "" {
		if err := os.Rename(tmppath, dst); err != nil {
			return fmt.Errorf("failed to move file in place: %w", err)
		}
		log.Printf("File moved to %q", dst)
		for _, action := range postRenameActions {
			if err := action(); err != nil {
				return fmt.Errorf("failed to run post-action: %w", err)
			}
		}
	} else {
		os.Remove(tmppath)
	}

	if _, err := i.configureNetwork(); err != nil {
		return fmt.Errorf("failed to configure network: %v", err)
	}

	if _, _, err := i.s.cfg.DB.MutateService(i.cfg.ServiceName, func(d *db.Data, s *db.Service) error {
		if s.ServiceType == "" {
			s.ServiceType = detectedServiceType
		} else if detectedServiceType != "" && s.ServiceType != detectedServiceType {
			return fmt.Errorf("service type mismatch: %v != %v", s.ServiceType, detectedServiceType)
		}
		if i.macvlan != nil {
			s.Macvlan = i.macvlan
		}
		if i.svcNet != nil {
			s.SvcNetwork = i.svcNet
		}
		if i.tsNet != nil {
			s.TSNet = i.tsNet
		}
		for a, p := range i.artifacts {
			af, ok := s.Artifacts[a]
			if !ok {
				af = &db.Artifact{
					Refs: map[db.ArtifactRef]string{},
				}
				mak.Set(&s.Artifacts, a, af)
			}
			af.Refs[db.ArtifactRef("staged")] = p
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update service: %w", err)
	}

	if i.cfg.StageOnly {
		return nil
	}

	i.printf("File received\n")
	i.printf("Installing service\n")
	si, err := i.s.NewInstaller(i.cfg.InstallerCfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	si.NewCmd = i.cfg.NewCmd
	if err := si.Install(); err != nil {
		return fmt.Errorf("failed to install service: %w", err)
	}
	i.printf("Service %q installed\n", i.cfg.ServiceName)
	return nil
}

func (i *FileInstaller) Fail() {
	i.failed = true
}

func (i *FileInstaller) tempFilePath() string {
	if i.tmpPath != "" {
		return i.tmpPath
	}
	return filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName),
		fmt.Sprintf("%s-%s.tmp", i.cfg.ServiceName, i.version()))
}

func (i *FileInstaller) version() string {
	if i.ver == "" {
		i.ver = fileutil.Version()
	}
	return i.ver
}

func (i *FileInstaller) tempPayloadName() string {
	name := strings.TrimSpace(i.cfg.PayloadName)
	if name != "" {
		base := filepath.Base(name)
		if base != "." && base != string(filepath.Separator) && base != ".." {
			return base
		}
	}
	return fmt.Sprintf("%s-%s.tmp", i.cfg.ServiceName, i.version())
}

func (i *FileInstaller) initTempFile() error {
	if i.tmpPath != "" {
		return nil
	}
	tmpDir, err := os.MkdirTemp(i.s.serviceBinDir(i.cfg.ServiceName),
		fmt.Sprintf("%s-%s-", i.cfg.ServiceName, i.version()))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	i.tmpDir = tmpDir
	i.tmpPath = filepath.Join(tmpDir, i.tempPayloadName())
	return nil
}

func (i *FileInstaller) cleanupTemp() {
	if i.tmpDir == "" {
		return
	}
	if err := os.RemoveAll(i.tmpDir); err != nil {
		log.Printf("failed to remove temp dir: %v", err)
	}
	i.tmpDir = ""
	i.tmpPath = ""
}

// NewInstaller returns a new SystemdInstaller for the given service
// name. The binary will be stored in the service's bin directory and installed
// as a service when closed.
func (s *Server) NewInstaller(cfg InstallerCfg) (*Installer, error) {
	si := &Installer{
		icfg: cfg,
		s:    s,

		NewCmd: cmdutil.NewStdCmd,
	}
	return si, nil
}

// Installer is an io.WriteCloser that writes the received binary to a file and
// installs the service when closed.
type Installer struct {
	NewCmd func(name string, arg ...string) *exec.Cmd

	icfg InstallerCfg
	s    *Server
}

func (si *Installer) printf(format string, args ...any) {
	if si.icfg.Printer != nil {
		si.icfg.Printer(format, args...)
	}
}

func unassignedIP(dv db.DataView) (netip.Addr, error) {
	isAssignedIP := func(ip netip.Addr) bool {
		for _, s := range dv.AsStruct().Services {
			if s.SvcNetwork != nil && s.SvcNetwork.IPv4 == ip {
				return true
			}
		}
		return false
	}
	ip := netip.MustParseAddr("192.168.100.3")
	pfx := netip.MustParsePrefix("192.168.100.0/24")
	max := netip.MustParseAddr("192.168.100.253")
	for isAssignedIP(ip) && ip.Less(max) {
		ip = ip.Next()
	}
	if !pfx.Contains(ip) || ip.Compare(max) > 0 {
		return netip.Addr{}, fmt.Errorf("no available IP address")
	}
	return ip, nil
}

func randomMAC() string {
	var b [6]byte
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	// Ensure the address is unicast and locally administered
	b[0] = (b[0] & 0xfe) | 0x02

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

const maxGenerations = 10

func (si *Installer) mutateService(f func(*db.Data, *db.Service) error) (*db.Data, *db.Service, error) {
	return si.s.cfg.DB.MutateService(si.icfg.ServiceName, f)
}

func (si *Installer) commitGen(gen int) (*db.Data, *db.Service, error) {
	d, s, err := si.mutateService(func(d *db.Data, s *db.Service) error {
		var srcRefName string
		var dstRefs []string
		if gen == 0 {
			s.LatestGeneration++
			s.Generation = s.LatestGeneration

			srcRefName = "staged"
			dstRefs = append(dstRefs, "latest", string(db.Gen(s.Generation)))
		} else {
			srcRefName = string(db.Gen(gen))
			dstRefs = append(dstRefs, "latest")
			s.Generation = gen
		}

		for _, refs := range s.Artifacts {
			val, ok := refs.Refs[db.ArtifactRef(srcRefName)]
			if !ok {
				continue
			}
			for _, ref := range dstRefs {
				refs.Refs[db.ArtifactRef(ref)] = val
			}
		}

		for rn, ir := range d.Images {
			if s, _, _ := strings.Cut(string(rn), "/"); s != si.icfg.ServiceName {
				log.Printf("skipping image %q", rn)
				continue
			}
			val, ok := ir.Refs[db.ImageRef(srcRefName)]
			if !ok {
				log.Printf("image %v:%v not found", rn, srcRefName)
				continue
			}
			for _, ref := range dstRefs {
				log.Printf("setting image %v:%v to %v:%v", rn, srcRefName, rn, ref)
				ir.Refs[db.ImageRef(ref)] = val
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to commit generation: %v", err)
	}
	return d, s, nil
}

func parseGenRef(ref db.ArtifactRef) (int, bool) {
	genStr, ok := strings.CutPrefix(string(ref), "gen-")
	if !ok {
		return 0, false
	}
	gen, err := strconv.Atoi(genStr)
	if err != nil {
		return 0, false
	}
	return gen, true
}

// Prune removes old configurations from the database.
func (si *Installer) prune() {
	knownBins := make(set.Set[string])
	// TODO(maisem): this should not be hardcoded here.
	knownBins.AddSlice([]string{"netns.env", "env", "main.ts", si.icfg.ServiceName})
	_, _, err := si.mutateService(func(d *db.Data, s *db.Service) error {
		minGen := s.LatestGeneration - maxGenerations
		for _, refs := range s.Artifacts {
			for ref, p := range refs.Refs {
				if gen, ok := parseGenRef(ref); !ok || gen >= minGen {
					knownBins.Add(filepath.Base(p))
				} else {
					delete(refs.Refs, ref)
				}
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("failed to mutate service: %v", err)
		return
	}

	bd := si.s.serviceBinDir(si.icfg.ServiceName)
	if err := keepOnlyKnownFilesInDir(bd, knownBins); err != nil {
		log.Printf("failed to keep only known files in %q: %v", bd, err)
	}
	ed := si.s.serviceEnvDir(si.icfg.ServiceName)
	if err := keepOnlyKnownFilesInDir(ed, knownBins); err != nil {
		log.Printf("failed to keep only known files in %q: %v", ed, err)
	}
}

func keepOnlyKnownFilesInDir(dir string, known set.Set[string]) error {
	// Loop over all files in the bin directory and remove any that are not in
	// the knownBins map.
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}
	for _, f := range files {
		if !known.Contains(f.Name()) {
			fp := filepath.Join(dir, f.Name())
			if err := os.Remove(fp); err != nil {
				log.Printf("failed to remove file: %v", err)
			} else {
				log.Printf("Removed old file: %s", fp)
			}
		}
	}
	return nil
}

/*
    TDDO: move to place where we write the file.
	isSelfUpdate := si.icfg.ServiceName == CatchService
	if isSelfUpdate && si.icfg.Artifact != "" {
		si.printf("Verifying catch binary\n")
		if err := verifyCatchBinary(si.icfg.Artifact); err != nil {
			si.printf("Failed to verify catch binary: %v\n", err)
			log.Printf("failed to verify catch binary: %v", err)
			return fmt.Errorf("failed to verify catch binary: %v", err)
		}
	}
*/

func (si *Installer) InstallGen(gen int) error {
	if runtime.GOOS == "darwin" {
		panic("macOS is not supported")
	}

	d, s, err := si.commitGen(gen)
	if err != nil {
		return fmt.Errorf("failed to commit gen: %v", err)
	}

	si.prune()

	return si.doInstall(d, s)
}

// Install installs the service.
func (si *Installer) Install() error {
	return si.InstallGen(0)
}

func (si *Installer) doInstall(d *db.Data, s *db.Service) error {
	if si.icfg.Pull && s.ServiceType != db.ServiceTypeDockerCompose {
		return fmt.Errorf("--pull is only valid for docker compose payloads")
	}
	switch s.ServiceType {
	case db.ServiceTypeSystemd:
		// Install and start the service.
		service, err := svc.NewSystemdService(si.s.cfg.DB, s.View(), si.s.serviceRunDir(si.icfg.ServiceName))
		if err != nil {
			return fmt.Errorf("failed to create service: %v", err)
		}
		if err := service.Install(); err != nil {
			return fmt.Errorf("failed to install service: %v", err)
		}
		if s.Name == CatchService && si.icfg.ClientCloser != nil {
			_ = si.icfg.ClientCloser.Close()
		}
		if err := service.Restart(); err != nil {
			return fmt.Errorf("failed to restart service: %v", err)
		}
	case db.ServiceTypeDockerCompose:
		if si.icfg.UI != nil {
			si.icfg.UI.Suspend()
		}
		// Check that docker is installed before trying to install
		if _, err := svc.DockerCmd(); err != nil {
			return err // svc.ErrDockerNotFound
		}
		service, err := svc.NewDockerComposeService(si.s.cfg.DB, s.View(), si.s.serviceDataDir(s.Name), si.s.serviceRunDir(s.Name))
		if err != nil {
			return fmt.Errorf("failed to create service: %v", err)
		}
		service.NewCmd = si.NewCmd
		if err := service.InstallWithPull(si.icfg.Pull); err != nil {
			return fmt.Errorf("failed to install service: %v", err)
		}

		err = service.UpWithPull(si.icfg.Pull)
		if err != nil {
			return fmt.Errorf("failed to up service: %v", err)
		}
	default:
		return fmt.Errorf("unknown service type: %v", s.ServiceType)
	}
	if s.LatestGeneration == 1 {
		si.s.PublishEvent(Event{
			Type:        EventTypeServiceCreated,
			ServiceName: s.Name,
			Data:        EventData{s.View()},
		})
	} else {
		si.s.PublishEvent(Event{
			Type:        EventTypeServiceConfigChanged,
			ServiceName: s.Name,
			Data:        EventData{s.View()},
		})
	}
	return nil
}

func asJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("failed to marshal: %v", err)
	}
	return string(b)
}

func verifyCatchBinary(path string) error {
	// Check if the file is a valid ELF binary.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer f.Close()
	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		return fmt.Errorf("failed to seek: %v", err)
	}
	// Execute the binary, passing "is-catch" as the first argument.
	out, err := exec.Command(path, "is-catch").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to execute binary: %v", err)
	}
	if string(out) != "yes\n" {
		return fmt.Errorf("not a catch binary")
	}
	return nil
}
