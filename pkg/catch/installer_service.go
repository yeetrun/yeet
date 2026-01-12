// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shayne/yeet/pkg/cmdutil"
	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/svc"
	"tailscale.com/util/set"
)

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
