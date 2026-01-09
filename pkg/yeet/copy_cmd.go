// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shayne/yeet/pkg/copyutil"
)

type copyEndpoint struct {
	Raw     string
	Path    string
	Service string
	Host    string
	Remote  bool
	DirHint bool
}

type copyRequest struct {
	Recursive bool
	Src       copyEndpoint
	Dst       copyEndpoint
}

func runCopyCommand(args []string, cfg *ProjectConfig) error {
	req, err := parseCopyArgs(args)
	if err != nil {
		return err
	}
	if req.Src.Remote && req.Dst.Remote {
		return fmt.Errorf("copy does not support remote-to-remote")
	}
	if !req.Src.Remote && !req.Dst.Remote {
		return fmt.Errorf("copy requires a service endpoint (svc:path)")
	}
	if err := applyCopyHostOverride(req, cfg); err != nil {
		return err
	}
	if req.Src.Remote {
		return copyFromRemote(req)
	}
	return copyToRemote(req)
}

func parseCopyArgs(args []string) (copyRequest, error) {
	var req copyRequest
	operands := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				operands = append(operands, args[i+1:]...)
			}
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			switch arg {
			case "-r", "-R", "--recursive":
				req.Recursive = true
				continue
			default:
				return copyRequest{}, fmt.Errorf("unknown flag %q", arg)
			}
		}
		operands = append(operands, arg)
	}
	if len(operands) != 2 {
		return copyRequest{}, fmt.Errorf("copy requires exactly two paths")
	}
	src, err := parseCopyEndpoint(operands[0])
	if err != nil {
		return copyRequest{}, err
	}
	dst, err := parseCopyEndpoint(operands[1])
	if err != nil {
		return copyRequest{}, err
	}
	req.Src = src
	req.Dst = dst
	return req, nil
}

func parseCopyEndpoint(raw string) (copyEndpoint, error) {
	ep := copyEndpoint{Raw: raw, Path: raw}
	idx := strings.Index(raw, ":")
	if idx <= 0 {
		return ep, nil
	}
	if isWindowsDrivePath(raw) || strings.ContainsAny(raw[:idx], "/"+string(os.PathSeparator)) {
		return ep, nil
	}
	servicePart := raw[:idx]
	if servicePart == "" {
		return copyEndpoint{}, fmt.Errorf("invalid remote spec %q", raw)
	}
	service, host, _ := splitServiceHost(servicePart)
	rel, dirHint, err := normalizeRemotePath(raw[idx+1:])
	if err != nil {
		return copyEndpoint{}, err
	}
	return copyEndpoint{
		Raw:     raw,
		Path:    rel,
		Service: service,
		Host:    host,
		Remote:  true,
		DirHint: dirHint,
	}, nil
}

func normalizeRemotePath(raw string) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	dirHint := trimmed == "" || trimmed == "." || trimmed == "./" || strings.HasSuffix(trimmed, "/")
	trimmed = strings.TrimPrefix(trimmed, "./")
	if trimmed == "." {
		trimmed = ""
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", dirHint, fmt.Errorf("remote path must be relative")
	}
	if trimmed == "data" || strings.HasPrefix(trimmed, "data/") {
		trimmed = strings.TrimPrefix(trimmed, "data")
		trimmed = strings.TrimPrefix(trimmed, "/")
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" {
		return "", true, nil
	}
	rel := path.Clean(trimmed)
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", dirHint, fmt.Errorf("invalid remote path %q", raw)
	}
	return rel, dirHint, nil
}

func isWindowsDrivePath(raw string) bool {
	if len(raw) < 3 {
		return false
	}
	if raw[1] != ':' {
		return false
	}
	return raw[2] == '\\' || raw[2] == '/'
}

func copyToRemote(req copyRequest) error {
	dst := req.Dst
	if !dst.Remote || dst.Service == "" {
		return fmt.Errorf("copy destination must be svc:path")
	}
	if dst.Host != "" {
		SetHostOverride(dst.Host)
	}
	srcPath := req.Src.Path
	if srcPath == "" {
		return fmt.Errorf("copy requires a source path")
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	destRel := dst.Path
	destDir := dst.DirHint
	if info.IsDir() {
		if !req.Recursive {
			return fmt.Errorf("%q is a directory (use -r)", srcPath)
		}
		prefix := ""
		if !hasTrailingSlash(req.Src.Raw) {
			prefix = filepath.Base(srcPath)
		}
		reader, err := tarDirectoryStream(srcPath, prefix)
		if err != nil {
			return err
		}
		defer reader.Close()
		args := []string{"copy", "--to", remotePathOrDot(destRel), "--archive"}
		return execRemoteFn(context.Background(), dst.Service, args, reader, false)
	}

	if destRel == "" || destDir {
		destRel = path.Join(destRel, filepath.Base(srcPath))
	}
	destRel, _, err = normalizeRemotePath(destRel)
	if err != nil || destRel == "" {
		return fmt.Errorf("invalid copy destination %q", dst.Raw)
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	args := []string{"copy", "--to", destRel}
	return execRemoteFn(context.Background(), dst.Service, args, f, false)
}

func applyCopyHostOverride(req copyRequest, cfg *ProjectConfig) error {
	hostOverride, hostOverrideSet := HostOverride()
	if hostOverrideSet && hostOverride != "" {
		return nil
	}
	remote := req.Dst
	if req.Src.Remote {
		remote = req.Src
	}
	if remote.Host != "" {
		SetHostOverride(remote.Host)
		return nil
	}
	host, err := resolveServiceHost(cfg, remote.Service)
	if err != nil {
		return err
	}
	if host != "" {
		SetHost(host)
	}
	return nil
}

func copyFromRemote(req copyRequest) error {
	src := req.Src
	if !src.Remote || src.Service == "" {
		return fmt.Errorf("copy source must be svc:path")
	}
	if src.Host != "" {
		SetHostOverride(src.Host)
	}
	srcRel := src.Path
	if srcRel == "" && !src.DirHint {
		return fmt.Errorf("copy requires a source path")
	}
	args := []string{"copy", "--from", remotePathOrDot(srcRel)}
	if req.Recursive {
		args = append(args, "--recursive")
	}
	reader, done, err := execRemoteStreamFn(context.Background(), src.Service, args, nil)
	if err != nil {
		return err
	}
	defer reader.Close()

	buf := bufio.NewReader(reader)
	kind, base, err := copyutil.ReadHeader(buf)
	if err != nil {
		<-done
		return err
	}
	destPath := req.Dst.Path
	if destPath == "" {
		<-done
		return fmt.Errorf("copy requires a destination path")
	}
	destDir := isLocalDirHint(destPath)
	if !destDir {
		if st, err := os.Stat(destPath); err == nil && st.IsDir() {
			destDir = true
		}
	}

	switch kind {
	case "file":
		if base == "" {
			<-done
			return fmt.Errorf("invalid copy header")
		}
		outPath := destPath
		if destDir {
			outPath = filepath.Join(destPath, base)
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			<-done
			return err
		}
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			<-done
			return err
		}
		if _, err := io.Copy(out, buf); err != nil {
			out.Close()
			<-done
			return err
		}
		if err := out.Close(); err != nil {
			<-done
			return err
		}
	case "dir":
		if !req.Recursive {
			<-done
			return fmt.Errorf("%q is a directory (use -r)", src.Raw)
		}
		root := destPath
		if destDir && base != "" && !src.DirHint {
			root = filepath.Join(destPath, base)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			<-done
			return err
		}
		if err := copyutil.ExtractTar(buf, root); err != nil {
			<-done
			return err
		}
	default:
		<-done
		return fmt.Errorf("invalid copy header")
	}

	if err := <-done; err != nil {
		return err
	}
	return nil
}

func tarDirectoryStream(src string, prefix string) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		err := copyutil.TarDirectory(pw, src, prefix)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return pr, nil
}

func remotePathOrDot(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func isLocalDirHint(path string) bool {
	if path == "" {
		return false
	}
	if path == "." || path == "./" || path == ".." || path == "../" {
		return true
	}
	if strings.HasSuffix(path, string(os.PathSeparator)) || strings.HasSuffix(path, "/") {
		return true
	}
	return false
}

func hasTrailingSlash(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasSuffix(path, string(os.PathSeparator)) || strings.HasSuffix(path, "/") {
		return true
	}
	return false
}
