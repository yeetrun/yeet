# Copy Multiple Sources Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet copy ~/.ssh/id* hermes:.ssh/` and VM remote glob downloads such as `yeet copy hermes:".ssh/id*" .ssh/` work with scp/rsync-style source parsing.

**Architecture:** Change the copy request model from a single source to multiple sources plus one destination. VM endpoints continue to use the existing rsync backend, now with multiple source operands. Regular service endpoints keep the existing yeet/catch single-item copy protocol and upload multiple local sources sequentially; regular-service remote globs stay unsupported with a clear error.

**Tech Stack:** Go, `pkg/yeet` client orchestration, `pkg/cli` command metadata, local rsync/ssh for VM copy, catch exec streaming for service-data copy, website MDX docs, GitButler (`but`) for version-control writes.

---

## File Structure

- Modify `pkg/yeet/copy_cmd.go`: multi-source request parsing, request validation, copy classification, service-data normalization, service-data sequential upload, VM rsync argument construction, regular-service remote-glob rejection.
- Modify `pkg/yeet/copy_cmd_test.go`: parser tests, request validation tests, classifier tests, service-data normalization tests, VM rsync argument tests, service-data upload tests, regular-service remote-glob tests.
- Modify `pkg/yeet/handle_svc_cmd_test.go`: end-to-end client handler regression for multi-source service-data uploads.
- Modify `pkg/cli/cli.go`: copy command usage and examples.
- Modify `cmd/yeet/cli_test.go`: copy help assertions.
- Modify `website/docs/cli/yeet-cli.mdx`: user-facing `yeet copy` multi-source and VM remote-glob docs.
- Modify `website/docs/payloads/vms.mdx`: VM-specific copy examples.

## Task 1: Parse Multiple Sources

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing parser tests**

Add these cases to `TestParseCopyArgs` in `pkg/yeet/copy_cmd_test.go`. Existing single-source expected values should also be updated from `Src: endpoint` to `Sources: []copyEndpoint{endpoint}` after the implementation step.

```go
{
	name: "multiple local sources remote destination",
	args: []string{"./id_ed25519", "./id_ed25519.pub", "devbox:.ssh/"},
	want: copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
		Sources: []copyEndpoint{
			{Raw: "./id_ed25519", Path: "./id_ed25519"},
			{Raw: "./id_ed25519.pub", Path: "./id_ed25519.pub"},
		},
		Dst: copyEndpoint{Raw: "devbox:.ssh/", Path: ".ssh/", Service: "devbox", Remote: true, DirHint: true},
	},
},
{
	name: "multiple vm remote sources local directory destination",
	args: []string{"devbox:.ssh/id_ed25519", "devbox:.ssh/id_ed25519.pub", "./keys/"},
	want: copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
		Sources: []copyEndpoint{
			{Raw: "devbox:.ssh/id_ed25519", Path: ".ssh/id_ed25519", Service: "devbox", Remote: true},
			{Raw: "devbox:.ssh/id_ed25519.pub", Path: ".ssh/id_ed25519.pub", Service: "devbox", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./keys/", Path: "./keys/"},
	},
},
{
	name: "vm remote glob source local directory destination",
	args: []string{"devbox:.ssh/id*", "./keys/"},
	want: copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
		Sources: []copyEndpoint{
			{Raw: "devbox:.ssh/id*", Path: ".ssh/id*", Service: "devbox", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./keys/", Path: "./keys/"},
	},
},
{
	name:    "missing destination",
	args:    []string{"./id_ed25519"},
	wantErr: "copy requires at least one source and one destination",
},
{
	name:    "multiple sources require directory destination",
	args:    []string{"./a", "./b", "svc:file.txt"},
	wantErr: "copy with multiple sources requires a directory destination",
},
{
	name:    "mixed local and remote sources rejected",
	args:    []string{"./a", "devbox:b", "./out/"},
	wantErr: "copy sources must all be local or all be from the same VM endpoint",
},
{
	name:    "multiple remote services rejected",
	args:    []string{"devbox:a", "other:b", "./out/"},
	wantErr: "copy sources must come from one VM endpoint",
},
```

- [ ] **Step 2: Run parser tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestParseCopyArgs' -count=1
```

Expected: FAIL because `copyRequest` has no `Sources` field and `finishCopyRequest` still requires exactly two operands.

- [ ] **Step 3: Change `copyRequest` and parser shape**

In `pkg/yeet/copy_cmd.go`, change `copyRequest`:

```go
type copyRequest struct {
	Recursive  bool
	Archive    bool
	Compress   bool
	Verbose    bool
	ForceProxy bool
	Sources    []copyEndpoint
	Dst        copyEndpoint
}
```

Replace `finishCopyRequest` with:

```go
func finishCopyRequest(req copyRequest, operands []string) (copyRequest, error) {
	if len(operands) < 2 {
		return copyRequest{}, fmt.Errorf("copy requires at least one source and one destination")
	}
	sources := make([]copyEndpoint, 0, len(operands)-1)
	for _, operand := range operands[:len(operands)-1] {
		src, err := parseCopyEndpoint(operand)
		if err != nil {
			return copyRequest{}, err
		}
		sources = append(sources, src)
	}
	dst, err := parseCopyEndpoint(operands[len(operands)-1])
	if err != nil {
		return copyRequest{}, err
	}
	req.Sources = sources
	req.Dst = dst
	if err := validateCopyRequestShape(req); err != nil {
		return copyRequest{}, err
	}
	return req, nil
}
```

Add request validation helpers:

```go
func validateCopyRequestShape(req copyRequest) error {
	if len(req.Sources) == 0 {
		return fmt.Errorf("copy requires at least one source and one destination")
	}
	if len(req.Sources) > 1 && !copyDestinationAllowsMultipleSources(req.Dst) {
		return fmt.Errorf("copy with multiple sources requires a directory destination")
	}
	return validateCopySourceSet(req.Sources)
}

func validateCopySourceSet(sources []copyEndpoint) error {
	remoteCount := 0
	var firstRemote copyEndpoint
	for _, src := range sources {
		if !src.Remote {
			continue
		}
		remoteCount++
		if remoteCount == 1 {
			firstRemote = src
			continue
		}
		if src.Service != firstRemote.Service || src.Host != firstRemote.Host {
			return fmt.Errorf("copy sources must come from one VM endpoint")
		}
	}
	if remoteCount > 0 && remoteCount != len(sources) {
		return fmt.Errorf("copy sources must all be local or all be from the same VM endpoint")
	}
	return nil
}

func copyDestinationAllowsMultipleSources(dst copyEndpoint) bool {
	if dst.Remote {
		return dst.DirHint
	}
	return isLocalDirHint(dst.Path) || existingLocalDirectory(dst.Path)
}
```

Add source access helpers:

```go
func firstCopySource(req copyRequest) copyEndpoint {
	if len(req.Sources) == 0 {
		return copyEndpoint{}
	}
	return req.Sources[0]
}

func singleCopySource(req copyRequest) (copyEndpoint, error) {
	if len(req.Sources) != 1 {
		return copyEndpoint{}, fmt.Errorf("copy requires exactly one source for this operation")
	}
	return req.Sources[0], nil
}
```

- [ ] **Step 4: Update existing single-source tests to the new request shape**

In `pkg/yeet/copy_cmd_test.go`, replace expected request fields like:

```go
Src: copyEndpoint{Raw: "local.txt", Path: "local.txt"},
```

with:

```go
Sources: []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
```

For tests that construct requests directly, use the same `Sources` field. Do not keep a compatibility `Src` field in `copyRequest`; `Sources` is the authoritative representation.

- [ ] **Step 5: Run parser tests and verify pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestParseCopyArgs|TestIsLocalDirHint' -count=1
```

Expected: PASS.

## Task 2: Classify And Normalize Multi-Source Requests

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing classification and normalization tests**

Update `TestClassifyCopyEndpoints` to construct requests with `Sources`. Add these cases:

```go
{
	name: "multiple local sources to remote",
	req: copyRequest{
		Sources: []copyEndpoint{
			{Raw: "a", Path: "a"},
			{Raw: "b", Path: "b"},
		},
		Dst: copyEndpoint{Raw: "svc:logs/", Path: "logs/", Service: "svc", Remote: true, DirHint: true},
	},
	wantDirection: copyDirectionToRemote,
	wantRemote:    copyEndpoint{Raw: "svc:logs/", Path: "logs/", Service: "svc", Remote: true, DirHint: true},
},
{
	name: "multiple remote sources from same service",
	req: copyRequest{
		Sources: []copyEndpoint{
			{Raw: "devbox:a", Path: "a", Service: "devbox", Remote: true},
			{Raw: "devbox:b", Path: "b", Service: "devbox", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
	},
	wantDirection: copyDirectionFromRemote,
	wantRemote:    copyEndpoint{Raw: "devbox:a", Path: "a", Service: "devbox", Remote: true},
},
```

Add normalization coverage to `TestNormalizeServiceDataCopyRequest`:

```go
{
	name: "download normalizes all remote sources",
	req: copyRequest{
		Sources: []copyEndpoint{
			{Raw: "svc:data/logs/a.txt", Path: "data/logs/a.txt", Service: "svc", Remote: true},
			{Raw: "svc:data/logs/b.txt", Path: "data/logs/b.txt", Service: "svc", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
	},
	want: copyRequest{
		Sources: []copyEndpoint{
			{Raw: "svc:data/logs/a.txt", Path: "logs/a.txt", Service: "svc", Remote: true},
			{Raw: "svc:data/logs/b.txt", Path: "logs/b.txt", Service: "svc", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
	},
},
```

- [ ] **Step 2: Run targeted tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestClassifyCopyEndpoints|TestNormalizeServiceDataCopyRequest' -count=1
```

Expected: FAIL because classifier and normalization still read the old single-source field.

- [ ] **Step 3: Update classifier and normalization code**

Replace `classifyCopyEndpoints` with:

```go
func classifyCopyEndpoints(req copyRequest) (copyDirection, copyEndpoint, error) {
	remoteSources := 0
	var firstRemote copyEndpoint
	for _, src := range req.Sources {
		if !src.Remote {
			continue
		}
		remoteSources++
		if remoteSources == 1 {
			firstRemote = src
		}
	}
	if remoteSources > 0 && req.Dst.Remote {
		return copyDirectionInvalid, copyEndpoint{}, fmt.Errorf("copy does not support remote-to-remote")
	}
	if remoteSources == 0 && !req.Dst.Remote {
		return copyDirectionInvalid, copyEndpoint{}, fmt.Errorf("copy requires a service endpoint (svc:path)")
	}
	if remoteSources > 0 {
		return copyDirectionFromRemote, firstRemote, nil
	}
	return copyDirectionToRemote, req.Dst, nil
}
```

Replace `normalizeServiceDataCopyRequest` with:

```go
func normalizeServiceDataCopyRequest(req copyRequest) (copyRequest, error) {
	for i, src := range req.Sources {
		if !src.Remote {
			continue
		}
		normalized, err := normalizeServiceDataEndpoint(src)
		if err != nil {
			return copyRequest{}, err
		}
		req.Sources[i] = normalized
	}
	if req.Dst.Remote {
		dst, err := normalizeServiceDataEndpoint(req.Dst)
		if err != nil {
			return copyRequest{}, err
		}
		req.Dst = dst
	}
	return req, nil
}
```

- [ ] **Step 4: Update call sites that read `req.Src`**

Replace direct `req.Src` reads with `firstCopySource(req)` only where the operation allows multiple sources, and `singleCopySource(req)` where the operation is intentionally single-source.

Concrete replacements:

```go
// old
return runVMRsyncCopyFunc(context.Background(), req, direction, remote, remoteCtx)

// unchanged call, but runVMRsyncCopy will read req.Sources
return runVMRsyncCopyFunc(context.Background(), req, direction, remote, remoteCtx)
```

```go
func localCopySource(req copyRequest) (string, error) {
	src, err := singleCopySource(req)
	if err != nil {
		return "", err
	}
	if src.Path == "" {
		return "", fmt.Errorf("copy requires a source path")
	}
	return src.Path, nil
}
```

```go
func remoteCopySource(req copyRequest) (copyEndpoint, error) {
	src, err := singleCopySource(req)
	if err != nil {
		return copyEndpoint{}, err
	}
	if !src.Remote || src.Service == "" {
		return copyEndpoint{}, fmt.Errorf("copy source must be svc:path")
	}
	if src.Path == "" && !src.DirHint {
		return copyEndpoint{}, fmt.Errorf("copy requires a source path")
	}
	return src, nil
}
```

```go
func copyDownloadArgs(req copyRequest) []string {
	src := firstCopySource(req)
	args := []string{"copy", "--from", remotePathOrDot(src.Path)}
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
```

- [ ] **Step 5: Run targeted tests and verify pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestClassifyCopyEndpoints|TestNormalizeServiceDataCopyRequest|TestCopyEndpointValidationHelpers|TestRemoteCopyCommandArgs' -count=1
```

Expected: PASS.

## Task 3: Support Multiple Sources In VM Rsync

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing VM rsync tests**

Add these tests to `pkg/yeet/copy_cmd_test.go`:

```go
func TestRunVMRsyncCopyUploadIncludesMultipleSources(t *testing.T) {
	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	var gotArgs []string
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Verbose:  true,
		Sources: []copyEndpoint{
			{Raw: "./id_ed25519", Path: "./id_ed25519"},
			{Raw: "./id_ed25519.pub", Path: "./id_ed25519.pub"},
		},
		Dst: copyEndpoint{Raw: "devbox:.ssh/", Path: ".ssh/", Service: "devbox", Remote: true, DirHint: true},
	}
	remoteCtx := testCopyVMRemoteContext("192.168.100.12")

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionToRemote, req.Dst, remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	assertSubsequence(t, gotArgs, []string{"-avz", "-e", "./id_ed25519", "./id_ed25519.pub", "yeet-pve1:.ssh/"})
}

func TestRunVMRsyncCopyDownloadPassesRemoteGlob(t *testing.T) {
	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	var gotArgs []string
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Verbose:  true,
		Sources: []copyEndpoint{
			{Raw: "devbox:.ssh/id*", Path: ".ssh/id*", Service: "devbox", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./keys/", Path: "./keys/"},
	}
	remoteCtx := testCopyVMRemoteContext("10.0.4.80")

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionFromRemote, req.Sources[0], remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	assertSubsequence(t, gotArgs, []string{"-avz", "-e", "yeet-pve1:.ssh/id*", "./keys/"})
}
```

Add helpers near the existing VM copy tests:

```go
func testCopyVMRemoteContext(sshHost string) copyRemoteContext {
	return copyRemoteContext{
		Host:   "yeet-pve1",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: sshHost},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: sshHost},
				},
			},
		},
	}
}

func assertSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	pos := 0
	for _, item := range got {
		if pos < len(want) && item == want[pos] {
			pos++
		}
	}
	if pos != len(want) {
		t.Fatalf("args = %#v, want subsequence %#v", got, want)
	}
}
```

- [ ] **Step 2: Run VM rsync tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunVMRsyncCopy.*Multiple|TestRunVMRsyncCopyDownloadPassesRemoteGlob' -count=1
```

Expected: FAIL because `vmRsyncArgs` still appends only one source.

- [ ] **Step 3: Update VM rsync arg construction**

Replace `vmRsyncArgs` with:

```go
func vmRsyncArgs(req copyRequest, direction copyDirection, remote copyEndpoint, plan sshExecutionPlan) ([]string, error) {
	remoteShell, target, err := rsyncRemoteShellAndTarget(plan)
	if err != nil {
		return nil, err
	}
	args := []string{copyRsyncFlags(req), "-e", remoteShell}
	switch direction {
	case copyDirectionToRemote:
		for _, src := range req.Sources {
			args = append(args, src.Path)
		}
		args = append(args, target+":"+remote.Path)
	case copyDirectionFromRemote:
		for _, src := range req.Sources {
			args = append(args, target+":"+src.Path)
		}
		args = append(args, req.Dst.Path)
	default:
		return nil, fmt.Errorf("invalid copy direction")
	}
	return args, nil
}
```

- [ ] **Step 4: Update existing VM rsync tests to use `Sources`**

For the existing upload test, replace:

```go
Src: copyEndpoint{Raw: "./local.txt", Path: "./local.txt"},
```

with:

```go
Sources: []copyEndpoint{{Raw: "./local.txt", Path: "./local.txt"}},
```

For the existing download test, replace:

```go
Src: copyEndpoint{Raw: "devbox:~/app.log", Path: "~/app.log", Service: "devbox", Remote: true},
```

with:

```go
Sources: []copyEndpoint{{Raw: "devbox:~/app.log", Path: "~/app.log", Service: "devbox", Remote: true}},
```

Change calls that pass `req.Src` to pass `req.Sources[0]`.

- [ ] **Step 5: Run VM rsync tests and verify pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunVMRsyncCopy|TestRunRsyncPlan|TestWithGuestRsyncHint' -count=1
```

Expected: PASS.

## Task 4: Support Sequential Multi-Source Service Uploads

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_test.go`

- [ ] **Step 1: Write failing service upload tests**

Add this test to `pkg/yeet/handle_svc_cmd_test.go` near the copy upload tests:

```go
func TestHandleSvcCmdCopyUploadMultipleFiles(t *testing.T) {
	stubCopyRegularServiceInfo(t)
	oldExec := execRemoteFn
	defer func() {
		execRemoteFn = oldExec
	}()

	tmp := t.TempDir()
	srcA := filepath.Join(tmp, "a.txt")
	srcB := filepath.Join(tmp, "b.txt")
	if err := os.WriteFile(srcA, []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(srcB, []byte("beta"), 0o600); err != nil {
		t.Fatalf("write b: %v", err)
	}

	var calls [][]string
	var names []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		calls = append(calls, append([]string{}, args...))
		gz, err := gzip.NewReader(stdin)
		if err != nil {
			t.Fatalf("gzip: %v", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, hdr.Name)
		return nil
	}

	if err := HandleSvcCmd([]string{"copy", srcA, srcB, "svc-a:incoming/"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	wantArgs := []string{"copy", "--to", "incoming", "--archive", "--compress"}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	for _, got := range calls {
		if !reflect.DeepEqual(got, wantArgs) {
			t.Fatalf("args = %#v, want %#v", got, wantArgs)
		}
	}
	if !reflect.DeepEqual(names, []string{"a.txt", "b.txt"}) {
		t.Fatalf("names = %#v, want a.txt b.txt", names)
	}
}
```

Add this error test to `pkg/yeet/copy_cmd_test.go`:

```go
func TestCopyServiceDataFromRemoteRejectsMultipleSources(t *testing.T) {
	err := copyServiceDataFromRemote(copyRequest{
		Sources: []copyEndpoint{
			{Raw: "svc:a", Path: "a", Service: "svc", Remote: true},
			{Raw: "svc:b", Path: "b", Service: "svc", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
	})
	if err == nil || !strings.Contains(err.Error(), "regular service copy downloads support one source") {
		t.Fatalf("error = %v, want one-source service download error", err)
	}
}
```

- [ ] **Step 2: Run service copy tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleSvcCmdCopyUploadMultipleFiles|TestCopyServiceDataFromRemoteRejectsMultipleSources' -count=1
```

Expected: FAIL because service upload still expects one source and service download error text is not implemented.

- [ ] **Step 3: Refactor service-data upload into sequential single-source uploads**

Replace `copyServiceDataToRemote` with:

```go
func copyServiceDataToRemote(req copyRequest) error {
	dst, err := remoteCopyDestination(req)
	if err != nil {
		return err
	}
	applyEndpointHostOverride(dst)
	for _, src := range req.Sources {
		single := req
		single.Sources = []copyEndpoint{src}
		if err := copySingleServiceDataToRemote(single, dst); err != nil {
			return fmt.Errorf("copy %s: %w", src.Raw, err)
		}
	}
	return nil
}

func copySingleServiceDataToRemote(req copyRequest, dst copyEndpoint) error {
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
```

Do not change `openCopyUpload`, `openDirectoryCopyUpload`, `openArchiveFileCopyUpload`, or `openPlainFileCopyUpload` beyond replacing their source reads with `singleCopySource(req)`.

- [ ] **Step 4: Update source readers**

Replace source reads in upload helpers:

```go
func localCopySource(req copyRequest) (string, error) {
	src, err := singleCopySource(req)
	if err != nil {
		return "", err
	}
	if src.Path == "" {
		return "", fmt.Errorf("copy requires a source path")
	}
	return src.Path, nil
}
```

```go
func openDirectoryCopyUpload(req copyRequest, report *copyReport) (copyUpload, error) {
	src := firstCopySource(req)
	prefix := sourceDirectoryArchivePrefix(src.Raw, src.Path)
	reader, err := tarDirectoryStream(src.Path, prefix, req.Compress, report.OnEntry)
	if err != nil {
		return copyUpload{}, err
	}
	return copyUpload{
		reader: reader,
		args:   copyUploadArgs(remotePathOrDot(req.Dst.Path), true, req.Compress),
	}, nil
}
```

```go
func openArchiveFileCopyUpload(req copyRequest, report *copyReport) (copyUpload, error) {
	src := firstCopySource(req)
	destRoot, entryName, err := remoteArchiveFileDestination(req.Dst.Path, req.Dst.DirHint, src.Path)
	if err != nil {
		return copyUpload{}, fmt.Errorf("invalid copy destination %q", req.Dst.Raw)
	}
	reader, err := tarFileStream(src.Path, entryName, req.Compress, report.OnEntry)
	if err != nil {
		return copyUpload{}, err
	}
	return copyUpload{
		reader: reader,
		args:   copyUploadArgs(remotePathOrDot(destRoot), true, req.Compress),
	}, nil
}
```

```go
func openPlainFileCopyUpload(req copyRequest) (copyUpload, error) {
	src := firstCopySource(req)
	destRel, err := remotePlainFileDestination(req.Dst.Path, req.Dst.DirHint, src.Path)
	if err != nil {
		return copyUpload{}, fmt.Errorf("invalid copy destination %q", req.Dst.Raw)
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return copyUpload{}, err
	}
	return copyUpload{
		reader: f,
		args:   copyUploadArgs(destRel, false, req.Compress),
	}, nil
}
```

- [ ] **Step 5: Reject regular-service multi-source downloads explicitly**

At the start of `copyServiceDataFromRemote`, add:

```go
func copyServiceDataFromRemote(req copyRequest) (err error) {
	if len(req.Sources) != 1 {
		return fmt.Errorf("regular service copy downloads support one source; use a VM endpoint for rsync-style remote multi-source copy")
	}
	src, err := remoteCopySource(req)
	if err != nil {
		return err
	}
	// existing body continues...
}
```

- [ ] **Step 6: Run service copy tests and verify pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleSvcCmdCopyUpload|TestCopyServiceDataFromRemoteRejectsMultipleSources|TestOpen.*CopyUpload|TestRemoteFileDestinations' -count=1
```

Expected: PASS.

## Task 5: Reject Regular-Service Remote Globs

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing remote-glob routing test**

Add this test to `pkg/yeet/copy_cmd_test.go`:

```go
func TestRunCopyCommandRejectsRegularServiceRemoteGlob(t *testing.T) {
	oldServerInfo := fetchSSHServerInfoFunc
	oldServiceInfo := fetchSSHServiceInfoFunc
	oldStream := execRemoteStreamFn
	oldHost := Host()
	defer func() {
		fetchSSHServerInfoFunc = oldServerInfo
		fetchSSHServiceInfoFunc = oldServiceInfo
		execRemoteStreamFn = oldStream
		SetHost(oldHost)
		resetHostOverride()
	}()
	resetHostOverride()
	SetHost("yeet-pve1")

	fetchSSHServerInfoFunc = func(context.Context, string) (serverInfo, error) {
		return serverInfo{}, nil
	}
	fetchSSHServiceInfoFunc = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{ServiceType: dockerServiceType}}, nil
	}
	execRemoteStreamFn = func(context.Context, string, []string, io.Reader) (io.ReadCloser, <-chan error, error) {
		t.Fatal("regular service remote glob should fail before copy")
		return nil, nil, nil
	}

	err := runCopyCommand([]string{"svc:logs/*.txt", "./logs/"}, nil)
	if err == nil || !strings.Contains(err.Error(), "remote globs are only supported for VM endpoints") {
		t.Fatalf("runCopyCommand error = %v, want regular-service remote glob error", err)
	}
}
```

- [ ] **Step 2: Run remote-glob test and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunCopyCommandRejectsRegularServiceRemoteGlob' -count=1
```

Expected: FAIL because regular service remote globs currently reach the normal service-data download path.

- [ ] **Step 3: Add service-data remote glob validation**

Add helpers:

```go
func validateServiceDataCopyShape(req copyRequest, direction copyDirection) error {
	if direction != copyDirectionFromRemote {
		return nil
	}
	if len(req.Sources) != 1 {
		return fmt.Errorf("regular service copy downloads support one source; use a VM endpoint for rsync-style remote multi-source copy")
	}
	if endpointHasGlob(firstCopySource(req)) {
		return fmt.Errorf("remote globs are only supported for VM endpoints; copy an exact service path or directory")
	}
	return nil
}

func endpointHasGlob(ep copyEndpoint) bool {
	return strings.ContainsAny(ep.Path, "*?[")
}
```

In `runCopyCommand`, after `normalizeServiceDataCopyRequest` and before dispatch:

```go
req, err = normalizeServiceDataCopyRequest(req)
if err != nil {
	return err
}
if err := validateServiceDataCopyShape(req, direction); err != nil {
	return err
}
if direction == copyDirectionFromRemote {
	return copyServiceDataFromRemote(req)
}
return copyServiceDataToRemote(req)
```

- [ ] **Step 4: Run targeted copy command tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunCopyCommand|TestCopyServiceDataFromRemoteRejectsMultipleSources' -count=1
```

Expected: PASS.

## Task 6: Update Help And Website Docs

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/vms.mdx`

- [ ] **Step 1: Write failing copy help assertion**

In `cmd/yeet/cli_test.go`, update `TestCopyHelpMentionsVMGuestCopy` to expect the new usage and examples:

```go
for _, want := range []string{
	"Copy files between local paths and service data or VM guests",
	"[--force-proxy] [-avz] <src>... <dst>",
	"yeet copy ./config.yml svc:data/config.yml",
	"yeet copy ~/.ssh/id* devbox:.ssh/",
	"yeet copy devbox:\".ssh/id*\" .ssh/",
	"yeet copy --force-proxy ./configs/ devbox:~/configs/",
} {
	if !strings.Contains(stdout, want) {
		t.Fatalf("copy help missing %q\n%s", want, stdout)
	}
}
```

- [ ] **Step 2: Run help test and verify failure**

Run:

```bash
mise exec -- go test ./cmd/yeet -run 'TestCopyHelpMentionsVMGuestCopy' -count=1
```

Expected: FAIL because help still says `<src> <dst>` and lacks multi-source examples.

- [ ] **Step 3: Update CLI metadata**

In `pkg/cli/cli.go`, update the copy command info:

```go
"copy": {Name: "copy", Description: "Copy files between local paths and service data or VM guests", Usage: "[--force-proxy] [-avz] <src>... <dst>", Examples: []string{
	"yeet copy ./config.yml svc:data/config.yml",
	"yeet copy ./configs/ svc:data/",
	"yeet copy svc:data/configs ./configs",
	"yeet copy ~/.ssh/id* devbox:.ssh/",
	"yeet copy devbox:\".ssh/id*\" .ssh/",
	"yeet copy ./app devbox:~/app",
	"yeet copy devbox:/var/log/cloud-init.log ./logs/",
	"yeet copy --force-proxy ./configs/ devbox:~/configs/",
}, Aliases: []string{"cp"}},
```

- [ ] **Step 4: Update website copy docs**

In `website/docs/cli/yeet-cli.mdx`, add multi-source and VM remote-glob examples inside the `### copy` examples block:

```mdx
# Multiple local sources -> VM guest directory
yeet copy ~/.ssh/id* devbox:.ssh/

# VM guest glob -> local directory
yeet copy devbox:".ssh/id*" .ssh/
```

Add notes:

```mdx
- Multiple sources use the scp/rsync convention: every operand except the last
  is a source, and the last operand is the destination.
- Multiple sources require a directory destination such as `svc:dir/`,
  `devbox:.ssh/`, or an existing local directory.
- Quoted remote globs such as `devbox:".ssh/id*"` are supported for VM
  endpoints because VM copy uses rsync. Regular service endpoints support exact
  service-data paths and directories, not remote globs.
```

In `website/docs/payloads/vms.mdx`, update the VM copy example block:

```mdx
yeet copy ./app devbox:~/app
yeet copy ~/.ssh/id* devbox:.ssh/
yeet copy devbox:".ssh/id*" .ssh/
yeet copy devbox:/var/log/cloud-init.log ./logs/
yeet copy --force-proxy ./configs/ devbox:~/configs/
```

- [ ] **Step 5: Run help and docs-adjacent tests**

Run:

```bash
mise exec -- go test ./cmd/yeet -run 'TestCopyHelpMentionsVMGuestCopy' -count=1
```

Expected: PASS.

## Task 7: Format, Test, And Checkpoint

**Files:**
- Modify: all files changed in previous tasks.

- [ ] **Step 1: Format Go code**

Run:

```bash
mise exec -- gofmt -w pkg/yeet/copy_cmd.go pkg/yeet/copy_cmd_test.go pkg/yeet/handle_svc_cmd_test.go pkg/cli/cli.go cmd/yeet/cli_test.go
```

Expected: command exits 0.

- [ ] **Step 2: Run targeted tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 4: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 5: Commit implementation checkpoint**

Run:

```bash
but status -fv
```

From the `but status -fv` output, commit exactly the change IDs for
`pkg/yeet/copy_cmd.go`, `pkg/yeet/copy_cmd_test.go`,
`pkg/yeet/handle_svc_cmd_test.go`, `pkg/cli/cli.go`,
`cmd/yeet/cli_test.go`, `website/docs/cli/yeet-cli.mdx`, and
`website/docs/payloads/vms.mdx`. Run `but commit
codex/copy-multiple-source-copy -m "copy: support multiple sources" --changes
...` with those comma-separated IDs and no unrelated file IDs.

Expected: one coherent implementation commit on
`codex/copy-multiple-source-copy`; no unrelated files included.

## Task 8: Live Smoke Test VM Copy

**Files:**
- No code changes unless smoke testing finds a bug.

- [ ] **Step 1: Create a disposable VM on pve1**

Create a uniquely named disposable VM so cleanup is unambiguous:

```bash
VM_NAME=codex-copy-smoke-$(date +%Y%m%d%H%M%S)
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet run "$VM_NAME" vm://ubuntu/26.04 --net=svc
```

Verify SSH readiness:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh "$VM_NAME" -- true
```

Expected: VM reaches running state and the SSH readiness command exits 0.

- [ ] **Step 2: Smoke test local multi-source upload**

Create temporary local files:

```bash
tmpdir=$(mktemp -d)
printf 'alpha\n' > "$tmpdir/a.txt"
printf 'beta\n' > "$tmpdir/b.txt"
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet copy "$tmpdir/a.txt" "$tmpdir/b.txt" "${VM_NAME}:/tmp/yeet-copy-smoke/"
```

Verify in the VM:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh "$VM_NAME" -- 'cat /tmp/yeet-copy-smoke/a.txt /tmp/yeet-copy-smoke/b.txt'
```

Expected output contains `alpha` and `beta`.

- [ ] **Step 3: Smoke test VM remote glob download**

Run:

```bash
outdir=$(mktemp -d)
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet copy "${VM_NAME}:/tmp/yeet-copy-smoke/*.txt" "$outdir/"
ls -1 "$outdir"
cat "$outdir/a.txt" "$outdir/b.txt"
```

Expected: `a.txt` and `b.txt` are present with the uploaded contents.

- [ ] **Step 4: Smoke test regular service remote glob rejection**

Create a disposable non-VM service and run the rejection check against it:

```bash
REGULAR_NAME=codex-copy-regular-$(date +%Y%m%d%H%M%S)
script=$(mktemp)
printf '#!/bin/sh\nsleep infinity\n' > "$script"
chmod +x "$script"
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet run "$REGULAR_NAME" "$script"
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet copy "${REGULAR_NAME}:logs/*.txt" "$outdir/"
```

Expected: command fails before streaming with `remote globs are only supported for VM endpoints`.

- [ ] **Step 5: Clean up disposable resources**

Remove the disposable VM and regular service:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet remove "$VM_NAME" --yes --clean-data --clean-config
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet remove "$REGULAR_NAME" --yes --clean-data --clean-config
```

Expected: `yeet status` no longer lists the disposable VM.

- [ ] **Step 6: Commit any smoke-test bug fixes**

If smoke testing required code fixes, run:

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet -count=1
pre-commit run --all-files
but status -fv
```

From the `but status -fv` output, commit exactly the change IDs for the smoke
test fix files. Run `but commit codex/copy-multiple-source-copy -m "copy: fix
live multi-source smoke issues" --changes ...` with those comma-separated IDs
and no unrelated file IDs.

Expected: only real smoke-test fixes are committed.
