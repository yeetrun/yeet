// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

type localUpgradeAction string

const (
	localUpgradeActionSkip   localUpgradeAction = "skip"
	localUpgradeActionUpdate localUpgradeAction = "update"
)

type localUpgradePlanResult struct {
	Action localUpgradeAction
	From   string
	To     string
	Reason string
}

var (
	currentExecutableFn       = os.Executable
	resolveYeetReleaseAssetFn = resolveYeetReleaseAsset
	downloadFileFn            = downloadFile
	extractSingleBinaryFn     = extractSingleBinary
	replaceLocalBinaryFn      = replaceLocalBinary
)

func localUpgradePlan(local buildinfo.Info, latest releaseCacheEntry) (localUpgradePlanResult, error) {
	result := localUpgradePlanResult{From: local.Version, To: latest.Tag}
	if latest.Tag == "" {
		result.Action = localUpgradeActionSkip
		result.Reason = "latest release is unknown"
		return result, nil
	}
	if !local.IsRelease() {
		result.Action = localUpgradeActionSkip
		result.Reason = "source/dev builds are not self-updated as release binaries"
		return result, nil
	}
	if buildinfo.CompareSemver(local.Version, latest.Tag) >= 0 {
		result.Action = localUpgradeActionSkip
		result.Reason = "already current"
		return result, nil
	}
	result.Action = localUpgradeActionUpdate
	return result, nil
}

func upgradeLocalBinary(local buildinfo.Info, latest releaseCacheEntry, yes bool) error {
	_ = yes
	plan, err := localUpgradePlan(local, latest)
	if err != nil || plan.Action != localUpgradeActionUpdate {
		return err
	}
	exe, err := currentExecutableFn()
	if err != nil {
		return fmt.Errorf("locate current yeet binary: %w", err)
	}
	assetName, assetURL, shaURL, _, err := resolveYeetReleaseAssetFn(runtime.GOOS, runtime.GOARCH, local.ReleaseChannel() == buildinfo.ChannelNightly)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "yeet-upgrade-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, assetName)
	shaPath := archivePath + ".sha256"
	if err := downloadFileFn(assetURL, archivePath); err != nil {
		return err
	}
	if err := downloadFileFn(shaURL, shaPath); err != nil {
		return err
	}
	if err := verifySHA256File(archivePath, shaPath); err != nil {
		return err
	}
	bin, err := extractSingleBinaryFn(archivePath, tmpDir)
	if err != nil {
		return err
	}
	return replaceLocalBinaryFn(exe, bin, !fileWritable(exe))
}

func downloadFile(url, path string) (err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(out, resp.Body)
	return err
}

func verifySHA256File(path, shaPath string) (err error) {
	raw, err := os.ReadFile(shaPath)
	if err != nil {
		return err
	}
	expected := strings.Fields(string(raw))
	if len(expected) == 0 {
		return fmt.Errorf("empty checksum file")
	}
	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected[0] {
		return fmt.Errorf("checksum mismatch")
	}
	return nil
}

func extractSingleBinary(archivePath, dstDir string) (path string, err error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := gz.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return extractSingleBinaryFromTar(tar.NewReader(gz), dstDir)
}

func extractSingleBinaryFromTar(tr *tar.Reader, dstDir string) (string, error) {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(hdr.Name)
		if !strings.HasPrefix(name, "yeet-") {
			continue
		}
		return writeExtractedBinary(tr, dstDir, name)
	}
	return "", fmt.Errorf("archive did not contain yeet binary")
}

func writeExtractedBinary(r io.Reader, dstDir, name string) (string, error) {
	outPath := filepath.Join(dstDir, name)
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return outPath, nil
}

func replaceLocalBinary(target, source string, sudo bool) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(target), ".yeet.upgrade.*")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if sudo {
		if err := exec.Command("sudo", "install", "-m", "0755", source, tmp).Run(); err != nil {
			return fmt.Errorf("sudo install replacement: %w", err)
		}
		if err := exec.Command("sudo", "mv", "-f", tmp, target).Run(); err != nil {
			return fmt.Errorf("sudo move replacement: %w", err)
		}
		return nil
	}
	if err := copyFileMode(source, tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func copyFileMode(source, target string, mode os.FileMode) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

func fileWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
