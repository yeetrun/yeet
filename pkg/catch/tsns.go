// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/env"
	"github.com/shayne/yeet/pkg/fileutil"
	"github.com/shayne/yeet/pkg/svc"
	"github.com/shayne/yeet/pkg/targz"
	"golang.org/x/oauth2/clientcredentials"
	"tailscale.com/client/tailscale"
	"tailscale.com/ipn"
	"tailscale.com/types/ptr"
)

func (s *Server) downloadTailscale(ver string) error {
	v, err := semver.NewVersion(ver)
	if err != nil {
		return err
	}
	track := "stable"
	if v.Minor()%2 == 1 {
		track = "unstable"
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	tarball := fmt.Sprintf("https://pkgs.tailscale.com/%s/tailscale_%s_%s.tgz", track, v.String(), runtime.GOARCH)

	// Fetch the tarball, verify the checksum, and extract it to the bin dir.
	resp, err := http.Get(tarball)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tgz, err := targz.New(resp.Body)
	if err != nil {
		return err
	}
	// Extract the binary to the bin dir.
	dstDir := filepath.Join(s.cfg.RootDir, "tsd")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	got := 0
	for {
		header, err := tgz.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fn := filepath.Base(header.Name)
		if fn == "tailscaled" || fn == "tailscale" {
			f, err := os.Create(filepath.Join(dstDir, fn+"-"+ver))
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(f, tgz)
			if err != nil {
				return err
			}
			if err := f.Chmod(0o755); err != nil {
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			got++
		}
	}
	if got != 2 {
		return fmt.Errorf("expected 2 binaries, got %d", got)
	}
	return nil
}

func (s *Server) getTailscaleBinary(ver string) (string, error) {
	dst := filepath.Join(s.cfg.RootDir, "tsd", "tailscale-"+ver)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := s.downloadTailscale(ver); err != nil {
		return "", err
	}
	return dst, nil
}

func (s *Server) getTailscaledBinary(ver string) (string, error) {
	dst := filepath.Join(s.cfg.RootDir, "tsd", "tailscaled-"+ver)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := s.downloadTailscale(ver); err != nil {
		return "", err
	}
	return dst, nil
}

type tsEnv struct {
	LogsDir string `env:"TS_LOGS_DIR"`
}

func extractOauthID(oauthSecret string) (string, bool) {
	// Based on https://tailscale.com/kb/1277/key-prefixes
	x, ok := strings.CutPrefix(oauthSecret, "tskey-client-")
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(x, "-")
	return id, ok
}

func tsClient(ctx context.Context) (*tailscale.Client, error) {
	b, err := os.ReadFile("tailscale.key")
	if err != nil {
		return nil, fmt.Errorf("failed to read tailscale.key: %w", err)
	}
	clientSecret := strings.TrimSpace(string(b))
	if !strings.HasPrefix(clientSecret, "tskey-client-") {
		return nil, errors.New("invalid tailscale oauth secret")
	}
	clientID, ok := extractOauthID(clientSecret)
	if !ok {
		return nil, errors.New("invalid oauth secret")
	}
	baseURL := cmp.Or(os.Getenv("TS_BASE_URL"), "https://api.tailscale.com")

	credentials := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     baseURL + "/api/v2/oauth/token",
		Scopes:       []string{"device"},
	}
	tailscale.I_Acknowledge_This_API_Is_Unstable = true

	tsClient := tailscale.NewClient("-", nil)
	tsClient.HTTPClient = credentials.Client(ctx)
	tsClient.BaseURL = baseURL
	return tsClient, nil
}

func generateTailscaleAuthKey(ctx context.Context, tags []string) (string, error) {
	tsClient, err := tsClient(ctx)
	if err != nil {
		return "", err
	}
	caps := tailscale.KeyCapabilities{
		Devices: tailscale.KeyDeviceCapabilities{
			Create: tailscale.KeyDeviceCreateCapabilities{
				Preauthorized: true,
				Tags:          tags,
			},
		},
	}

	authkey, _, err := tsClient.CreateKey(ctx, caps)
	if err != nil {
		return "", err
	}
	return authkey, nil
}

func (s *Server) getTailscaleAuthKey(ctx context.Context, tags []string) (string, error) {
	return generateTailscaleAuthKey(ctx, tags)
}

// installTS installs a Tailscale service. If runInNetNS is empty, it runs
// Tailscale in TAP mode. Otherwise, it runs Tailscale TUN mode in the specified
// netns. In TUN mode, Tailscale unit will depend on the netns service unit.
func (s *Server) installTS(service string, runInNetNS string, tsNet *db.TailscaleNetwork, tsAuthKey, resolvConf string) (map[db.ArtifactName]string, error) {
	if tsAuthKey == "" {
		ak, err := s.getTailscaleAuthKey(context.TODO(), tsNet.Tags)
		if err != nil {
			return nil, err
		}
		tsAuthKey = ak
	}
	tsd, err := s.getTailscaledBinary(tsNet.Version)
	if err != nil {
		return nil, err
	}
	serviceTSDir := filepath.Join(s.serviceRootDir(service), "tailscale")
	if err := os.MkdirAll(serviceTSDir, 0o755); err != nil {
		return nil, err
	}
	te := tsEnv{
		LogsDir: serviceTSDir,
	}
	envFile := filepath.Join(serviceTSDir, fileutil.ApplyVersion("tailscaled.env"))
	if err := env.Write(envFile, &te); err != nil {
		return nil, fmt.Errorf("failed to write env: %v", err)
	}
	binDir := s.serviceBinDir(service)

	runDir := s.serviceRunDir(service)
	unit := svc.SystemdUnit{
		Name:       "yeet-" + service + "-ts",
		Executable: filepath.Join(runDir, "tailscaled"),
		Arguments: []string{
			"--statedir=.",
			"--socket=" + filepath.Join(runDir, "tailscaled.sock"),
			"--config=" + filepath.Join(runDir, "tailscaled.json"),
		},
		EnvFile:          filepath.Join(runDir, "tailscaled.env"),
		WorkingDirectory: serviceTSDir,
	}
	if runInNetNS == "" {
		unit.Arguments = append(unit.Arguments, "--tun=tap:"+tsNet.Interface)
	} else {
		unit.Arguments = append(unit.Arguments, "--tun="+tsNet.Interface)
		unit.Requires = runInNetNS + ".service"
		unit.NetNS = runInNetNS
		unit.ResolvConf = resolvConf
	}
	artifacts, err := unit.WriteOutUnitFiles(binDir)
	if err != nil {
		return nil, fmt.Errorf("failed to write unit files: %v", err)
	}

	tsCfg := ipn.ConfigVAlpha{
		Version:  "alpha0",
		Hostname: ptr.To(service),
		AuthKey:  ptr.To(tsAuthKey),
		Locked:   "false",
	}
	if tsNet.ExitNode != "" {
		tsCfg.ExitNode = ptr.To(tsNet.ExitNode)
	}
	b, err := json.Marshal(tsCfg)
	if err != nil {
		return nil, fmt.Errorf("error marshalling tailscaled config: %w", err)
	}
	tsCfgFile := filepath.Join(serviceTSDir, fileutil.ApplyVersion("tailscaled.json"))
	if err := os.WriteFile(tsCfgFile, b, 0o644); err != nil {
		return nil, fmt.Errorf("error writing tailscaled config: %w", err)
	}
	artifacts[db.ArtifactTSConfig] = tsCfgFile
	artifacts[db.ArtifactTSService] = artifacts[db.ArtifactSystemdUnit]
	delete(artifacts, db.ArtifactSystemdUnit)
	artifacts[db.ArtifactTSEnv] = envFile
	artifacts[db.ArtifactTSBinary] = tsd
	return artifacts, nil
}
