// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/env"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
	"github.com/yeetrun/yeet/pkg/targz"
	"tailscale.com/client/tailscale/v2"
	"tailscale.com/ipn"
	"tailscale.com/types/ptr"
)

const tailscalePackageBaseURL = "https://pkgs.tailscale.com"

var tailscaleHTTPClient = http.DefaultClient

type tailscaleDownload struct {
	version string
	track   string
	goarch  string
	url     string
}

func newTailscaleDownload(ver, goos, goarch string) (tailscaleDownload, error) {
	v, err := semver.NewVersion(ver)
	if err != nil {
		return tailscaleDownload{}, err
	}
	track := "stable"
	if v.Minor()%2 == 1 {
		track = "unstable"
	}
	if goos != "linux" {
		return tailscaleDownload{}, fmt.Errorf("unsupported OS: %s", goos)
	}

	return tailscaleDownload{
		version: v.String(),
		track:   track,
		goarch:  goarch,
		url:     tailscaleArchiveURL(track, v.String(), goarch),
	}, nil
}

func tailscaleArchiveURL(track, ver, goarch string) string {
	return fmt.Sprintf("%s/%s/tailscale_%s_%s.tgz", tailscalePackageBaseURL, track, ver, goarch)
}

func (s *Server) downloadTailscale(ver string) error {
	dl, err := newTailscaleDownload(ver, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	dstDir := filepath.Join(s.cfg.RootDir, "tsd")
	return downloadTailscaleArchive(tailscaleHTTPClient, dl.url, dstDir, ver)
}

func downloadTailscaleArchive(client *http.Client, tarball, dstDir, ver string) (retErr error) {
	resp, err := client.Get(tarball)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	return extractTailscaleBinaries(resp.Body, dstDir, ver)
}

func extractTailscaleBinaries(r io.Reader, dstDir, ver string) (retErr error) {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	tgz, err := targz.New(r)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := tgz.Close(); retErr == nil {
			retErr = closeErr
		}
	}()

	got := 0
	for {
		header, err := tgz.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := extractTailscaleBinary(header, tgz, dstDir, ver); err != nil {
			return err
		}
		if isTailscaleBinary(header.Name) {
			got++
		}
	}
	if got != 2 {
		return fmt.Errorf("expected 2 binaries, got %d", got)
	}
	return nil
}

func extractTailscaleBinary(header *tar.Header, r io.Reader, dstDir, ver string) (retErr error) {
	fn := filepath.Base(header.Name)
	if !isTailscaleBinary(fn) {
		return nil
	}
	f, err := os.Create(filepath.Join(dstDir, fn+"-"+ver))
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Chmod(0o755)
}

func isTailscaleBinary(name string) bool {
	fn := filepath.Base(name)
	return fn == "tailscaled" || fn == "tailscale"
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
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid TS_BASE_URL %q: %w", baseURL, err)
	}

	tsClient := &tailscale.Client{
		BaseURL: parsedBaseURL,
		Tailnet: "-",
		Auth: &tailscale.OAuth{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{"device"},
		},
	}
	return tsClient, nil
}

func generateTailscaleAuthKey(ctx context.Context, tags []string) (string, error) {
	tsClient, err := tsClient(ctx)
	if err != nil {
		return "", err
	}
	caps := tailscale.KeyCapabilities{}
	caps.Devices.Create.Preauthorized = true
	caps.Devices.Create.Tags = tags

	key, err := tsClient.Keys().CreateAuthKey(ctx, tailscale.CreateKeyRequest{
		Capabilities: caps,
	})
	if err != nil {
		return "", err
	}
	return key.Key, nil
}

func (s *Server) getTailscaleAuthKey(ctx context.Context, tags []string) (string, error) {
	return generateTailscaleAuthKey(ctx, tags)
}

// installTS installs a Tailscale service. If runInNetNS is empty, it runs
// Tailscale in TAP mode. Otherwise, it runs Tailscale TUN mode in the specified
// netns. In TUN mode, Tailscale unit will depend on the netns service unit.
func (s *Server) installTS(service string, runInNetNS string, tsNet *db.TailscaleNetwork, tsAuthKey, resolvConf string) (map[db.ArtifactName]string, error) {
	tsAuthKey, err := s.resolveTailscaleAuthKey(tsNet, tsAuthKey)
	if err != nil {
		return nil, err
	}
	tsd, err := s.getTailscaledBinary(tsNet.Version)
	if err != nil {
		return nil, err
	}
	serviceTSDir := filepath.Join(s.serviceRootDir(service), "tailscale")
	if err := os.MkdirAll(serviceTSDir, 0o755); err != nil {
		return nil, err
	}
	envFile, err := writeTailscaleEnv(serviceTSDir)
	if err != nil {
		return nil, fmt.Errorf("failed to write env: %v", err)
	}
	unit := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service:       service,
		runDir:        s.serviceRunDir(service),
		serviceTSDir:  serviceTSDir,
		runInNetNS:    runInNetNS,
		interfaceName: tsNet.Interface,
		resolvConf:    resolvConf,
	})
	artifacts, err := unit.WriteOutUnitFiles(s.serviceBinDir(service))
	if err != nil {
		return nil, fmt.Errorf("failed to write unit files: %v", err)
	}

	tsCfgFile, err := writeTailscaleConfig(serviceTSDir, service, tsAuthKey, tsNet.ExitNode)
	if err != nil {
		return nil, err
	}
	artifacts[db.ArtifactTSConfig] = tsCfgFile
	artifacts[db.ArtifactTSService] = artifacts[db.ArtifactSystemdUnit]
	delete(artifacts, db.ArtifactSystemdUnit)
	artifacts[db.ArtifactTSEnv] = envFile
	artifacts[db.ArtifactTSBinary] = tsd
	return artifacts, nil
}

func (s *Server) resolveTailscaleAuthKey(tsNet *db.TailscaleNetwork, tsAuthKey string) (string, error) {
	if tsAuthKey != "" {
		return tsAuthKey, nil
	}
	return s.getTailscaleAuthKey(context.TODO(), tsNet.Tags)
}

func writeTailscaleEnv(serviceTSDir string) (string, error) {
	envFile := filepath.Join(serviceTSDir, fileutil.ApplyVersion("tailscaled.env"))
	te := tsEnv{LogsDir: serviceTSDir}
	return envFile, env.Write(envFile, &te)
}

type tailscaleInstallPlan struct {
	service       string
	runDir        string
	serviceTSDir  string
	runInNetNS    string
	interfaceName string
	resolvConf    string
}

func newTailscaleSystemdUnit(plan tailscaleInstallPlan) svc.SystemdUnit {
	unit := svc.SystemdUnit{
		Name:             "yeet-" + plan.service + "-ts",
		Executable:       filepath.Join(plan.runDir, "tailscaled"),
		Arguments:        tailscaleSystemdArgs(plan.runDir, plan.interfaceName, plan.runInNetNS == ""),
		EnvFile:          filepath.Join(plan.runDir, "tailscaled.env"),
		WorkingDirectory: plan.serviceTSDir,
	}
	if plan.runInNetNS != "" {
		applyTailscaleNetNS(&unit, plan.runInNetNS, plan.resolvConf)
	}
	return unit
}

func tailscaleSystemdArgs(runDir, interfaceName string, tapMode bool) []string {
	tunArg := "--tun=" + interfaceName
	if tapMode {
		tunArg = "--tun=tap:" + interfaceName
	}
	return []string{
		"--statedir=.",
		"--socket=" + filepath.Join(runDir, "tailscaled.sock"),
		"--config=" + filepath.Join(runDir, "tailscaled.json"),
		tunArg,
	}
}

func applyTailscaleNetNS(unit *svc.SystemdUnit, runInNetNS, resolvConf string) {
	nsUnit := runInNetNS + ".service"
	unit.Wants = nsUnit
	unit.After = nsUnit
	unit.ExecStartPre = []string{"/bin/systemctl is-active --quiet " + nsUnit}
	unit.NetNS = runInNetNS
	unit.ResolvConf = resolvConf
}

func writeTailscaleConfig(serviceTSDir, service, tsAuthKey, exitNode string) (string, error) {
	b, err := json.Marshal(tailscaleConfig(service, tsAuthKey, exitNode))
	if err != nil {
		return "", fmt.Errorf("error marshalling tailscaled config: %w", err)
	}
	tsCfgFile := filepath.Join(serviceTSDir, fileutil.ApplyVersion("tailscaled.json"))
	if err := os.WriteFile(tsCfgFile, b, 0o644); err != nil {
		return "", fmt.Errorf("error writing tailscaled config: %w", err)
	}
	return tsCfgFile, nil
}

func tailscaleConfig(service, tsAuthKey, exitNode string) ipn.ConfigVAlpha {
	tsCfg := ipn.ConfigVAlpha{
		Version:  "alpha0",
		Hostname: ptr.To(service),
		AuthKey:  ptr.To(tsAuthKey),
		Locked:   "false",
	}
	if exitNode != "" {
		tsCfg.ExitNode = ptr.To(exitNode)
	}
	return tsCfg
}
