// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/copyutil"
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
	Archive   bool
	Compress  bool
	Verbose   bool
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
	req := copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
	}
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
			if strings.HasPrefix(arg, "--") {
				switch arg {
				case "--recursive":
					req.Recursive = true
					continue
				case "--archive":
					req.Archive = true
					req.Recursive = true
					continue
				case "--compress":
					req.Compress = true
					continue
				case "--verbose":
					req.Verbose = true
					continue
				default:
					return copyRequest{}, fmt.Errorf("unknown flag %q", arg)
				}
			}
			if len(arg) > 2 {
				for _, flag := range arg[1:] {
					switch flag {
					case 'r', 'R':
						req.Recursive = true
					case 'a':
						req.Archive = true
						req.Recursive = true
					case 'z':
						req.Compress = true
					case 'v':
						req.Verbose = true
					default:
						return copyRequest{}, fmt.Errorf("unknown flag %q", arg)
					}
				}
				continue
			}
			switch arg {
			case "-r", "-R":
				req.Recursive = true
				continue
			case "-a":
				req.Archive = true
				req.Recursive = true
				continue
			case "-z":
				req.Compress = true
				continue
			case "-v":
				req.Verbose = true
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
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}

	destRel := dst.Path
	destDir := dst.DirHint
	report := newCopyReport(req.Verbose)
	report.Start("sending")

	var reader io.ReadCloser
	var args []string

	if info.IsDir() {
		prefix := ""
		if !hasTrailingSlash(req.Src.Raw) {
			prefix = filepath.Base(srcPath)
		}
		reader, err = tarDirectoryStream(srcPath, prefix, req.Compress, report.OnEntry)
		if err != nil {
			return err
		}
		args = []string{"copy", "--to", remotePathOrDot(destRel), "--archive"}
		if req.Compress {
			args = append(args, "--compress")
		}
	} else if req.Archive {
		destRoot := destRel
		entryName := filepath.Base(srcPath)
		if destRel != "" && !destDir {
			entryName = path.Base(destRel)
			destRoot = path.Dir(destRel)
			if destRoot == "." {
				destRoot = ""
			}
		}
		destRoot, _, err = normalizeRemotePath(destRoot)
		if err != nil {
			return fmt.Errorf("invalid copy destination %q", dst.Raw)
		}
		reader, err = tarFileStream(srcPath, entryName, req.Compress, report.OnEntry)
		if err != nil {
			return err
		}
		args = []string{"copy", "--to", remotePathOrDot(destRoot), "--archive"}
		if req.Compress {
			args = append(args, "--compress")
		}
	} else {
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
		reader = f
		args = []string{"copy", "--to", destRel}
		if req.Compress {
			args = append(args, "--compress")
		}
	}

	defer reader.Close()
	counter := &countingReader{r: reader}
	if err := execRemoteFn(context.Background(), dst.Service, args, counter, false); err != nil {
		return err
	}
	report.Finish(counter.N(), 0)
	return nil
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
	if req.Archive {
		args = append(args, "--archive")
	}
	if req.Compress {
		args = append(args, "--compress")
	}
	if req.Recursive && !req.Archive {
		args = append(args, "--recursive")
	}
	reader, done, err := execRemoteStreamFn(context.Background(), src.Service, args, nil)
	if err != nil {
		return err
	}
	defer reader.Close()

	report := newCopyReport(req.Verbose)
	report.Start("receiving")

	counter := &countingReader{r: reader}
	buf := bufio.NewReader(counter)
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

	payload := io.Reader(buf)
	var gz *gzip.Reader
	if req.Compress {
		var err error
		gz, err = gzip.NewReader(buf)
		if err != nil {
			<-done
			return err
		}
		defer gz.Close()
		payload = gz
	}

	switch kind {
	case "file":
		parent := filepath.Dir(destPath)
		if destDir {
			parent = destPath
		}
		if err := os.MkdirAll(parent, 0o755); err != nil {
			<-done
			return err
		}
		staged, stageDir, err := extractToTemp(parent, payload, report.OnEntry)
		if err != nil {
			<-done
			return err
		}
		defer os.RemoveAll(stageDir)
		baseName := base
		if baseName == "" {
			baseName = filepath.Base(staged)
		}
		outPath := destPath
		if destDir {
			outPath = filepath.Join(destPath, baseName)
		}
		if err := replaceLocalPath(staged, outPath); err != nil {
			<-done
			return err
		}
	case "dir":
		root := destPath
		if destDir && base != "" && !src.DirHint {
			root = filepath.Join(destPath, base)
		}
		if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
			<-done
			return err
		}
		stageDir, err := os.MkdirTemp(filepath.Dir(root), "yeet-copy-*")
		if err != nil {
			<-done
			return err
		}
		defer os.RemoveAll(stageDir)
		if err := copyutil.ExtractTarWithOptions(payload, stageDir, copyutil.ExtractOptions{OnEntry: report.OnEntry}); err != nil {
			<-done
			return err
		}
		if err := copyutil.MoveTree(stageDir, root); err != nil {
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
	report.Finish(0, counter.N())
	return nil
}

func tarDirectoryStream(src string, prefix string, compress bool, observer copyutil.TarObserver) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go writeTarStream(pw, compress, func(w io.Writer) error {
		return copyutil.TarDirectoryWithObserver(w, src, prefix, observer)
	})
	return pr, nil
}

func tarFileStream(src string, name string, compress bool, observer copyutil.TarObserver) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go writeTarStream(pw, compress, func(w io.Writer) error {
		return copyutil.TarFileWithObserver(w, src, name, observer)
	})
	return pr, nil
}

func writeTarStream(pw *io.PipeWriter, compress bool, fn func(io.Writer) error) {
	var (
		err error
		gz  *gzip.Writer
		w   io.Writer = pw
	)
	if compress {
		gz = gzip.NewWriter(pw)
		w = gz
	}
	err = fn(w)
	if gz != nil {
		if cerr := gz.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}
	if err != nil {
		_ = pw.CloseWithError(err)
		return
	}
	_ = pw.Close()
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) N() int64 {
	return c.n
}

type copyReport struct {
	verbose   bool
	startedAt time.Time
	total     int64
}

func newCopyReport(verbose bool) *copyReport {
	return &copyReport{verbose: verbose}
}

func (r *copyReport) Start(direction string) {
	if !r.verbose {
		return
	}
	fmt.Fprintf(os.Stdout, "%s incremental file list\n", direction)
	r.startedAt = time.Now()
}

func (r *copyReport) OnEntry(entry copyutil.TarEntry) {
	if entry.Type == tar.TypeReg || entry.Type == 0 {
		r.total += entry.Size
	}
	if !r.verbose {
		return
	}
	name := entry.Name
	if entry.Type == tar.TypeDir {
		name += "/"
	}
	if name != "" {
		fmt.Fprintln(os.Stdout, name)
	}
}

func (r *copyReport) Finish(sentBytes, receivedBytes int64) {
	if !r.verbose {
		return
	}
	if r.startedAt.IsZero() {
		r.startedAt = time.Now()
	}
	elapsed := time.Since(r.startedAt).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}
	totalBytes := sentBytes + receivedBytes
	rate := int64(float64(totalBytes) / elapsed)
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintf(os.Stdout, "sent %s bytes  received %s bytes  %s bytes/sec\n",
		formatBytesShort(sentBytes),
		formatBytesShort(receivedBytes),
		formatBytesShort(rate),
	)
	speedup := 1.0
	if totalBytes > 0 {
		speedup = float64(r.total) / float64(totalBytes)
	}
	fmt.Fprintf(os.Stdout, "total size is %s  speedup is %.2f\n", formatBytesShort(r.total), speedup)
}

func formatBytesShort(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	units := []string{"K", "M", "G", "T", "P", "E"}
	val := float64(n)
	unit := ""
	for _, u := range units {
		val /= 1000
		unit = u
		if val < 1000 {
			break
		}
	}
	return fmt.Sprintf("%.2f%s", val, unit)
}

func extractToTemp(parent string, r io.Reader, observer copyutil.TarObserver) (string, string, error) {
	stageDir, err := os.MkdirTemp(parent, "yeet-copy-*")
	if err != nil {
		return "", "", err
	}
	var staged string
	var stagedCount int
	err = copyutil.ExtractTarWithOptions(r, stageDir, copyutil.ExtractOptions{OnEntry: func(entry copyutil.TarEntry) {
		if observer != nil {
			observer(entry)
		}
		if entry.Type != tar.TypeDir && stagedCount == 0 {
			staged = filepath.Join(stageDir, filepath.FromSlash(entry.Name))
		}
		if entry.Type != tar.TypeDir {
			stagedCount++
		}
	}})
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return "", "", err
	}
	if stagedCount != 1 {
		_ = os.RemoveAll(stageDir)
		return "", "", fmt.Errorf("expected single file in archive, found %d", stagedCount)
	}
	return staged, stageDir, nil
}

func replaceLocalPath(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
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
