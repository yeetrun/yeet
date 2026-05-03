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

type copyDirection int

const (
	copyDirectionInvalid copyDirection = iota
	copyDirectionToRemote
	copyDirectionFromRemote
)

func runCopyCommand(args []string, cfg *ProjectConfig) error {
	req, err := parseCopyArgs(args)
	if err != nil {
		return err
	}
	direction, remote, err := classifyCopyEndpoints(req)
	if err != nil {
		return err
	}
	if err := applyCopyHostOverrideForEndpoint(remote, cfg); err != nil {
		return err
	}
	if direction == copyDirectionFromRemote {
		return copyFromRemote(req)
	}
	return copyToRemote(req)
}

func classifyCopyEndpoints(req copyRequest) (copyDirection, copyEndpoint, error) {
	if req.Src.Remote && req.Dst.Remote {
		return copyDirectionInvalid, copyEndpoint{}, fmt.Errorf("copy does not support remote-to-remote")
	}
	if !req.Src.Remote && !req.Dst.Remote {
		return copyDirectionInvalid, copyEndpoint{}, fmt.Errorf("copy requires a service endpoint (svc:path)")
	}
	if req.Src.Remote {
		return copyDirectionFromRemote, req.Src, nil
	}
	return copyDirectionToRemote, req.Dst, nil
}

func parseCopyArgs(args []string) (copyRequest, error) {
	req := defaultCopyRequest()
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
			if err := applyCopyFlag(&req, arg); err != nil {
				return copyRequest{}, fmt.Errorf("unknown flag %q", arg)
			}
			continue
		}
		operands = append(operands, arg)
	}
	return finishCopyRequest(req, operands)
}

func defaultCopyRequest() copyRequest {
	return copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
	}
}

func applyCopyFlag(req *copyRequest, arg string) error {
	if strings.HasPrefix(arg, "--") {
		return applyLongCopyFlag(req, arg)
	}
	for _, flag := range arg[1:] {
		if err := applyShortCopyFlag(req, flag); err != nil {
			return err
		}
	}
	return nil
}

func applyLongCopyFlag(req *copyRequest, arg string) error {
	switch arg {
	case "--recursive":
		req.Recursive = true
	case "--archive":
		req.Archive = true
		req.Recursive = true
	case "--compress":
		req.Compress = true
	case "--verbose":
		req.Verbose = true
	default:
		return fmt.Errorf("unknown flag")
	}
	return nil
}

func applyShortCopyFlag(req *copyRequest, flag rune) error {
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
		return fmt.Errorf("unknown flag")
	}
	return nil
}

func finishCopyRequest(req copyRequest, operands []string) (copyRequest, error) {
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
	trimmed, dirHint := trimRemotePath(raw)
	if strings.HasPrefix(trimmed, "/") {
		return "", dirHint, fmt.Errorf("remote path must be relative")
	}
	trimmed = trimRemoteDataPrefix(trimmed)
	if trimmed == "" {
		return "", true, nil
	}
	rel := cleanRemotePath(trimmed)
	if escapesRemoteRoot(rel) {
		return "", dirHint, fmt.Errorf("invalid remote path %q", raw)
	}
	return rel, dirHint, nil
}

func trimRemotePath(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	dirHint := trimmed == "" || trimmed == "." || trimmed == "./" || strings.HasSuffix(trimmed, "/")
	trimmed = strings.TrimPrefix(trimmed, "./")
	if trimmed == "." {
		trimmed = ""
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	return trimmed, dirHint
}

func trimRemoteDataPrefix(remotePath string) string {
	if remotePath == "data" {
		return ""
	}
	if strings.HasPrefix(remotePath, "data/") {
		return strings.TrimLeft(strings.TrimPrefix(remotePath, "data/"), "/")
	}
	return remotePath
}

func cleanRemotePath(remotePath string) string {
	rel := path.Clean(remotePath)
	if rel == "." {
		return ""
	}
	return rel
}

func escapesRemoteRoot(remotePath string) bool {
	return remotePath == ".." || strings.HasPrefix(remotePath, "../")
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

type copyUpload struct {
	reader io.ReadCloser
	args   []string
}

func copyToRemote(req copyRequest) error {
	dst, err := remoteCopyDestination(req)
	if err != nil {
		return err
	}
	applyEndpointHostOverride(dst)

	info, err := localCopySourceInfo(req)
	if err != nil {
		return err
	}

	report := newCopyReport(req.Verbose)
	report.Start("sending")
	upload, err := openCopyUpload(req, info, report)
	if err != nil {
		return err
	}
	return sendCopyUpload(dst, upload, report)
}

func sendCopyUpload(dst copyEndpoint, upload copyUpload, report *copyReport) error {
	counter := &countingReader{r: upload.reader}
	err := execRemoteFn(context.Background(), dst.Service, upload.args, counter, false)
	closeErr := upload.reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	report.Finish(counter.N(), 0)
	return nil
}

func remoteCopyDestination(req copyRequest) (copyEndpoint, error) {
	if !req.Dst.Remote || req.Dst.Service == "" {
		return copyEndpoint{}, fmt.Errorf("copy destination must be svc:path")
	}
	return req.Dst, nil
}

func localCopySource(req copyRequest) (string, error) {
	if req.Src.Path == "" {
		return "", fmt.Errorf("copy requires a source path")
	}
	return req.Src.Path, nil
}

func localCopySourceInfo(req copyRequest) (os.FileInfo, error) {
	srcPath, err := localCopySource(req)
	if err != nil {
		return nil, err
	}
	return os.Lstat(srcPath)
}

func applyEndpointHostOverride(ep copyEndpoint) {
	if ep.Host != "" {
		SetHostOverride(ep.Host)
	}
}

func openCopyUpload(req copyRequest, info os.FileInfo, report *copyReport) (copyUpload, error) {
	if info.IsDir() {
		return openDirectoryCopyUpload(req, report)
	}
	if req.Archive {
		return openArchiveFileCopyUpload(req, report)
	}
	return openPlainFileCopyUpload(req)
}

func openDirectoryCopyUpload(req copyRequest, report *copyReport) (copyUpload, error) {
	prefix := sourceDirectoryArchivePrefix(req.Src.Raw, req.Src.Path)
	reader, err := tarDirectoryStream(req.Src.Path, prefix, req.Compress, report.OnEntry)
	if err != nil {
		return copyUpload{}, err
	}
	return copyUpload{
		reader: reader,
		args:   copyUploadArgs(remotePathOrDot(req.Dst.Path), true, req.Compress),
	}, nil
}

func openArchiveFileCopyUpload(req copyRequest, report *copyReport) (copyUpload, error) {
	destRoot, entryName, err := remoteArchiveFileDestination(req.Dst.Path, req.Dst.DirHint, req.Src.Path)
	if err != nil {
		return copyUpload{}, fmt.Errorf("invalid copy destination %q", req.Dst.Raw)
	}
	reader, err := tarFileStream(req.Src.Path, entryName, req.Compress, report.OnEntry)
	if err != nil {
		return copyUpload{}, err
	}
	return copyUpload{
		reader: reader,
		args:   copyUploadArgs(remotePathOrDot(destRoot), true, req.Compress),
	}, nil
}

func openPlainFileCopyUpload(req copyRequest) (copyUpload, error) {
	destRel, err := remotePlainFileDestination(req.Dst.Path, req.Dst.DirHint, req.Src.Path)
	if err != nil {
		return copyUpload{}, fmt.Errorf("invalid copy destination %q", req.Dst.Raw)
	}
	f, err := os.Open(req.Src.Path)
	if err != nil {
		return copyUpload{}, err
	}
	return copyUpload{
		reader: f,
		args:   copyUploadArgs(destRel, false, req.Compress),
	}, nil
}

func sourceDirectoryArchivePrefix(raw, srcPath string) string {
	if hasTrailingSlash(raw) {
		return ""
	}
	return filepath.Base(srcPath)
}

func remoteArchiveFileDestination(destRel string, destDir bool, srcPath string) (string, string, error) {
	destRoot := destRel
	entryName := filepath.Base(srcPath)
	if destRel != "" && !destDir {
		entryName = path.Base(destRel)
		destRoot = path.Dir(destRel)
		if destRoot == "." {
			destRoot = ""
		}
	}
	destRoot, _, err := normalizeRemotePath(destRoot)
	return destRoot, entryName, err
}

func remotePlainFileDestination(destRel string, destDir bool, srcPath string) (string, error) {
	if destRel == "" || destDir {
		destRel = path.Join(destRel, filepath.Base(srcPath))
	}
	destRel, _, err := normalizeRemotePath(destRel)
	if err != nil {
		return "", err
	}
	if destRel == "" {
		return "", fmt.Errorf("empty remote file path")
	}
	return destRel, nil
}

func copyUploadArgs(dest string, archive bool, compress bool) []string {
	args := []string{"copy", "--to", dest}
	if archive {
		args = append(args, "--archive")
	}
	if compress {
		args = append(args, "--compress")
	}
	return args
}

func applyCopyHostOverrideForEndpoint(remote copyEndpoint, cfg *ProjectConfig) error {
	if hasActiveHostOverride() {
		return nil
	}
	if applyRemoteEndpointHost(remote) {
		return nil
	}
	return applyConfiguredCopyHost(cfg, remote.Service)
}

func hasActiveHostOverride() bool {
	hostOverride, hostOverrideSet := HostOverride()
	return hostOverrideSet && hostOverride != ""
}

func applyRemoteEndpointHost(remote copyEndpoint) bool {
	if remote.Host == "" {
		return false
	}
	SetHostOverride(remote.Host)
	return true
}

func applyConfiguredCopyHost(cfg *ProjectConfig, service string) error {
	host, err := resolveServiceHost(cfg, service)
	if err != nil {
		return err
	}
	if host != "" {
		SetHost(host)
	}
	return nil
}

func copyFromRemote(req copyRequest) (err error) {
	src, err := remoteCopySource(req)
	if err != nil {
		return err
	}
	applyEndpointHostOverride(src)

	reader, done, err := execRemoteStreamFn(context.Background(), src.Service, copyDownloadArgs(req), nil)
	if err != nil {
		return err
	}
	defer captureCloseError(&err, reader)

	report := newCopyReport(req.Verbose)
	report.Start("receiving")

	counter := &countingReader{r: reader}
	if err := receiveRemoteCopy(req, counter, done, report); err != nil {
		return err
	}
	report.Finish(0, counter.N())
	return nil
}

func remoteCopySource(req copyRequest) (copyEndpoint, error) {
	if !req.Src.Remote || req.Src.Service == "" {
		return copyEndpoint{}, fmt.Errorf("copy source must be svc:path")
	}
	if req.Src.Path == "" && !req.Src.DirHint {
		return copyEndpoint{}, fmt.Errorf("copy requires a source path")
	}
	return req.Src, nil
}

func copyDownloadArgs(req copyRequest) []string {
	args := []string{"copy", "--from", remotePathOrDot(req.Src.Path)}
	if req.Archive {
		args = append(args, "--archive")
	}
	if req.Compress {
		args = append(args, "--compress")
	}
	if req.Recursive && !req.Archive {
		args = append(args, "--recursive")
	}
	return args
}

func receiveRemoteCopy(req copyRequest, r io.Reader, done <-chan error, report *copyReport) (err error) {
	receive, err := prepareRemoteCopyReceive(req, r)
	if err != nil {
		waitRemoteCopy(done)
		return err
	}
	if receive.closer != nil {
		defer captureCloseError(&err, receive.closer)
	}
	if err := extractRemoteCopyPayload(receive.header, receive.dest, req.Src.DirHint, receive.payload, report.OnEntry); err != nil {
		waitRemoteCopy(done)
		return err
	}
	return <-done
}

type remoteCopyReceive struct {
	header  remoteCopyHeader
	dest    localCopyTarget
	payload io.Reader
	closer  io.Closer
}

func prepareRemoteCopyReceive(req copyRequest, r io.Reader) (remoteCopyReceive, error) {
	buf := bufio.NewReader(r)
	header, err := readRemoteCopyHeader(buf)
	if err != nil {
		return remoteCopyReceive{}, err
	}
	dest, err := localCopyDestination(req.Dst.Path)
	if err != nil {
		return remoteCopyReceive{}, err
	}
	payload, closer, err := remoteCopyPayload(buf, req.Compress)
	if err != nil {
		return remoteCopyReceive{}, err
	}
	return remoteCopyReceive{header: header, dest: dest, payload: payload, closer: closer}, nil
}

type remoteCopyHeader struct {
	kind string
	base string
}

type localCopyTarget struct {
	path string
	dir  bool
}

func readRemoteCopyHeader(r *bufio.Reader) (remoteCopyHeader, error) {
	kind, base, err := copyutil.ReadHeader(r)
	return remoteCopyHeader{kind: kind, base: base}, err
}

func localCopyDestination(destPath string) (localCopyTarget, error) {
	if destPath == "" {
		return localCopyTarget{}, fmt.Errorf("copy requires a destination path")
	}
	dest := localCopyTarget{path: destPath, dir: isLocalDirHint(destPath)}
	if !dest.dir {
		dest.dir = existingLocalDirectory(destPath)
	}
	return dest, nil
}

func existingLocalDirectory(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func remoteCopyPayload(r *bufio.Reader, compress bool) (io.Reader, io.Closer, error) {
	if !compress {
		return r, nil, nil
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return gz, gz, nil
}

func extractRemoteCopyPayload(header remoteCopyHeader, dest localCopyTarget, sourceDirHint bool, payload io.Reader, observer copyutil.TarObserver) error {
	switch header.kind {
	case "file":
		return extractRemoteFileCopy(dest, header.base, payload, observer)
	case "dir":
		return extractRemoteDirCopy(dest, header.base, sourceDirHint, payload, observer)
	default:
		return fmt.Errorf("invalid copy header")
	}
}

func extractRemoteFileCopy(dest localCopyTarget, base string, payload io.Reader, observer copyutil.TarObserver) (err error) {
	parent := filepath.Dir(dest.path)
	if dest.dir {
		parent = dest.path
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staged, stageDir, err := extractToTemp(parent, payload, observer)
	if err != nil {
		return err
	}
	defer captureRemoveAllError(&err, stageDir)
	return replaceLocalPath(staged, localFileOutputPath(dest, base, staged))
}

func localFileOutputPath(dest localCopyTarget, base, staged string) string {
	if !dest.dir {
		return dest.path
	}
	if base == "" {
		base = filepath.Base(staged)
	}
	return filepath.Join(dest.path, base)
}

func extractRemoteDirCopy(dest localCopyTarget, base string, sourceDirHint bool, payload io.Reader, observer copyutil.TarObserver) (err error) {
	root := localDirOutputPath(dest, base, sourceDirHint)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(root), "yeet-copy-*")
	if err != nil {
		return err
	}
	defer captureRemoveAllError(&err, stageDir)
	if err := copyutil.ExtractTarWithOptions(payload, stageDir, copyutil.ExtractOptions{OnEntry: observer}); err != nil {
		return err
	}
	return copyutil.MoveTree(stageDir, root)
}

func localDirOutputPath(dest localCopyTarget, base string, sourceDirHint bool) string {
	if dest.dir && base != "" && !sourceDirHint {
		return filepath.Join(dest.path, base)
	}
	return dest.path
}

func waitRemoteCopy(done <-chan error) {
	<-done
}

func captureCloseError(errp *error, closer io.Closer) {
	if closeErr := closer.Close(); *errp == nil && closeErr != nil {
		*errp = closeErr
	}
}

func captureRemoveAllError(errp *error, path string) {
	if removeErr := os.RemoveAll(path); *errp == nil && removeErr != nil {
		*errp = removeErr
	}
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
	_, _ = fmt.Fprintf(os.Stdout, "%s incremental file list\n", direction)
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
		_, _ = fmt.Fprintln(os.Stdout, name)
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
	_, _ = fmt.Fprintln(os.Stdout, "")
	_, _ = fmt.Fprintf(os.Stdout, "sent %s bytes  received %s bytes  %s bytes/sec\n",
		formatBytesShort(sentBytes),
		formatBytesShort(receivedBytes),
		formatBytesShort(rate),
	)
	speedup := 1.0
	if totalBytes > 0 {
		speedup = float64(r.total) / float64(totalBytes)
	}
	_, _ = fmt.Fprintf(os.Stdout, "total size is %s  speedup is %.2f\n", formatBytesShort(r.total), speedup)
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
