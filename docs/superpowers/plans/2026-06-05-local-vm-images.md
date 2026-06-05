# Local VM Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add catch-local VM image imports so users can upload a local VM bundle with `yeet vm images import foo/bar ./bundle`, run it as `vm://foo/bar`, and remove it after testing.

**Architecture:** Keep `yeet run` simple: it only resolves and runs `vm://...` image names. Add a client-side import command that streams a tar archive to catch over the existing exec WebSocket stdin path; catch validates and canonicalizes the bundle into its VM image cache, records a named catch-local ref, and makes `vm://foo/bar` resolve to the imported immutable content version. Rootfs-only imports use the current yeet-managed kernel and Firecracker from `vm://ubuntu/26.04`; local kernels are allowed only when the import command passes `--allow-local-kernel`.

**Tech Stack:** Go CLI parser in `pkg/cli`, client orchestration in `pkg/yeet`, catch-side command handling in `pkg/catch`, tar streaming/extraction through `pkg/copyutil`, existing catchrpc exec stdin streaming, README and website docs.

---

## File Structure

- Modify `pkg/cli/cli.go`: extend VM images flags, help text, parser validation, and command metadata for `import`, `rm`, and `ls`.
- Modify `pkg/cli/cli_test.go`: parser and metadata tests for new VM image subcommands and flags.
- Modify `cmd/yeet/cli_bridge_test.go`: ensure VM image subcommands still skip service-argument bridging and preserve local import args.
- Modify `pkg/yeet/svc_cmd.go`: intercept `yeet vm images import <name> <dir>` on the client, tar the local bundle, and stream it to catch as `vm images import <name> --stdin`.
- Create `pkg/yeet/vm_images_import_test.go`: client-side import streaming tests.
- Modify `pkg/catch/tty_vm.go`: route the new VM images actions through `vmImagesCmdFunc`.
- Modify `pkg/catch/vm_images_cmd.go`: implement `ls`, `import`, and `rm` command dispatch and rendering.
- Create `pkg/catch/vm_images_local.go`: local image name validation, ref storage, bundle extraction, canonical manifest generation, content hashing, GC on remove, and local image resolution helpers.
- Create `pkg/catch/vm_images_local_test.go`: unit tests for import validation, canonicalization, local kernel gating, remove, and resolver behavior.
- Modify `pkg/catch/vm_image.go`: refactor image resolution from URL-only to source-aware resolution for built-in remote images and catch-local imported images.
- Modify `pkg/catch/vm_image_test.go`: tests for `vm://foo/bar` resolving to a local imported ref and clear unknown-image errors.
- Modify `pkg/catch/vm_provision_test.go`: tests that `runVM` accepts `vm://foo/bar` once a local ref exists and stores the exact imported version.
- Modify `README.md`: document the local VM image lifecycle.
- Modify `website/docs/payloads/vms.mdx`: document import/run/remove workflow, bundle layout, and local kernel flag.
- Modify `website/docs/cli/yeet-cli.mdx`: update CLI examples for `yeet vm images`.

## Data Model

Imported image root: `<catch-root>/vm-images/local`

Reference files:

```text
<catch-root>/vm-images/local/refs/<name segments>/ref.json
```

Content directories:

```text
<catch-root>/vm-images/local/blobs/<sha256-hex>/
  manifest.json
  rootfs.ext4 or rootfs.ext4.zst
  vmlinux
  firecracker
  checksums.txt
```

Reference JSON:

```json
{
  "name": "foo/bar",
  "payload": "vm://foo/bar",
  "version": "local-foo-bar-0123456789ab",
  "contentID": "0123456789abcdef...",
  "root": "/root/data/vm-images/local/blobs/0123456789abcdef...",
  "rootfs": "rootfs.ext4.zst",
  "kernel": "vmlinux",
  "firecracker": "firecracker",
  "kernelPolicy": "yeet-managed",
  "createdAt": "2026-06-05T00:00:00Z"
}
```

Local image names use lowercase path segments:

```text
foo
foo/bar
team/ubuntu-fast
```

Reject:

```text
ubuntu/26.04
../foo
/foo
foo//bar
foo/bar/
foo bar
Foo/Bar
```

`ubuntu/*` is reserved for yeet built-ins.

---

### Task 1: CLI Parser And Help Metadata

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`

- [ ] **Step 1: Write parser tests for VM images import, rm, and ls**

Add these tests to `pkg/cli/cli_test.go` near `TestParseVMImages`.

```go
func TestParseVMImagesImport(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"import", "foo/bar", "./bundle", "--allow-local-kernel", "--format=json"})
	if err != nil {
		t.Fatalf("ParseVMImages import: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("format = %q, want json", flags.Format)
	}
	if !flags.AllowLocalKernel {
		t.Fatal("AllowLocalKernel = false, want true")
	}
	if flags.Stdin {
		t.Fatal("Stdin = true for public import args, want false")
	}
	want := []string{"import", "foo/bar", "./bundle"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesImportStdin(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"import", "foo/bar", "--stdin", "--format=json-pretty"})
	if err != nil {
		t.Fatalf("ParseVMImages import stdin: %v", err)
	}
	if !flags.Stdin {
		t.Fatal("Stdin = false, want true")
	}
	if flags.Format != "json-pretty" {
		t.Fatalf("format = %q, want json-pretty", flags.Format)
	}
	want := []string{"import", "foo/bar"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesRemove(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"rm", "foo/bar", "--yes", "--format=table"})
	if err != nil {
		t.Fatalf("ParseVMImages rm: %v", err)
	}
	if !flags.Yes {
		t.Fatal("Yes = false, want true")
	}
	if flags.Format != "table" {
		t.Fatalf("format = %q, want table", flags.Format)
	}
	want := []string{"rm", "foo/bar"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesListAlias(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"ls", "--output=json"})
	if err != nil {
		t.Fatalf("ParseVMImages ls: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("format = %q, want json", flags.Format)
	}
	want := []string{"ls"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}
```

- [ ] **Step 2: Run parser tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/cli -run 'TestParseVMImages' -count=1
```

Expected: FAIL because `VMImagesFlags` does not have `AllowLocalKernel`, `Stdin`, or `Yes`.

- [ ] **Step 3: Implement parser flags and command metadata**

In `pkg/cli/cli.go`, replace the `VMImagesFlags` type with:

```go
type VMImagesFlags struct {
	Format           string
	AllowLocalKernel bool
	Stdin            bool
	Yes              bool
}
```

Extend `vmImagesFlagsParsed` with:

```go
AllowLocalKernel bool   `flag:"allow-local-kernel" usage:"Allow an imported VM image bundle to provide vmlinux"`
Stdin            bool   `flag:"stdin" usage:"Read an import bundle tar stream from stdin"`
Yes              bool   `flag:"yes" short:"y" usage:"Skip confirmation prompts"`
Format           string `flag:"format" usage:"Output format: table, json, json-pretty"`
Output           string `flag:"output" usage:"Alias for --format"`
```

Keep `ParseVMImages` format normalization and return:

```go
flags := VMImagesFlags{
	Format:           format,
	AllowLocalKernel: parsed.Flags.AllowLocalKernel,
	Stdin:            parsed.Flags.Stdin,
	Yes:              parsed.Flags.Yes,
}
```

Update VM images command metadata:

```go
Usage: "vm images [ls|update|import <name> <dir>|rm <name>] [--format=table|json|json-pretty]"
Examples: []string{
	"yeet vm images",
	"yeet vm images ls",
	"yeet vm images update",
	"yeet vm images import foo/bar ./dist/my-vm",
	"yeet vm images import kernel/test ./dist/my-vm --allow-local-kernel",
	"yeet vm images rm foo/bar --yes",
}
```

- [ ] **Step 4: Update bridge test expectations**

In `cmd/yeet/cli_bridge_test.go`, add a case near the VM images bridge test:

```go
func TestBridgeServiceArgsSkipsVMImagesImport(t *testing.T) {
	service, host, args, bridged, err := bridgeServiceArgs([]string{"vm", "images", "import", "foo/bar", "./bundle"})
	if err != nil {
		t.Fatalf("bridgeServiceArgs: %v", err)
	}
	if bridged || service != "" || host != "" {
		t.Fatalf("vm images import should not bridge service args, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	want := []string{"vm", "images", "import", "foo/bar", "./bundle"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}
```

If `bridgeServiceArgs` is not directly available under that name, use the existing helper used by `TestBridgeServiceArgsSkipsVMImages`.

- [ ] **Step 5: Run CLI tests to verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseVMImages|TestBridgeServiceArgsSkipsVMImages' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge_test.go
mise exec -- git commit -m "cli: add VM image import commands"
```

Expected: commit succeeds.

---

### Task 2: Client-Side Local Bundle Upload

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Create: `pkg/yeet/vm_images_import_test.go`

- [ ] **Step 1: Write failing client import tests**

Create `pkg/yeet/vm_images_import_test.go`:

```go
package yeet

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHandleSvcVMImagesImportStreamsBundleToCatch(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	var gotService string
	var gotArgs []string
	var gotPayload bytes.Buffer
	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string(nil), args...)
		gotTTY = tty
		if stdin == nil {
			t.Fatal("stdin = nil, want tar stream")
		}
		if _, err := io.Copy(&gotPayload, stdin); err != nil {
			t.Fatalf("copy stdin: %v", err)
		}
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", dir}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	if gotService != systemServiceName {
		t.Fatalf("service = %q, want %s", gotService, systemServiceName)
	}
	wantArgs := []string{"vm", "images", "import", "foo/bar", "--stdin"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotTTY {
		t.Fatal("tty = true, want false for tar upload")
	}
	assertTarContains(t, gotPayload.Bytes(), "rootfs.ext4")
}

func TestHandleSvcVMImagesImportPassesAllowLocalKernel(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string(nil), args...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", dir, "--allow-local-kernel"}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	want := []string{"vm", "images", "import", "foo/bar", "--stdin", "--allow-local-kernel"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestHandleSvcVMImagesImportRejectsMissingDirectory(t *testing.T) {
	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", filepath.Join(t.TempDir(), "missing")}},
		Service: systemServiceName,
	})
	if err == nil || !strings.Contains(err.Error(), "VM image bundle directory does not exist") {
		t.Fatalf("error = %v, want missing bundle directory", err)
	}
}

func TestHandleSvcVMImagesImportNonImportDelegatesRemote(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "ls"}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	want := []string{"vm", "images", "ls"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func assertTarContains(t *testing.T, raw []byte, want string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if hdr.Name == want {
			return
		}
	}
	t.Fatalf("tar missing %q", want)
}
```

- [ ] **Step 2: Run client tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleSvcVMImagesImport' -count=1
```

Expected: FAIL because `handleSvcVM` does not exist and `svcCommandHandlers` does not intercept `vm`.

- [ ] **Step 3: Implement client intercept and streaming**

In `pkg/yeet/svc_cmd.go`, add `"vm"` to `svcCommandHandlers`:

```go
"vm": func(ctx context.Context, req svcCommandRequest) error {
	return handleSvcVM(ctx, req)
},
```

Add:

```go
func handleSvcVM(ctx context.Context, req svcCommandRequest) error {
	args := req.Command.RawArgs
	if len(args) >= 3 && args[0] == "vm" && args[1] == "images" && args[2] == "import" {
		return handleVMImagesImport(ctx, args[3:])
	}
	return handleSvcRemote(ctx, req)
}

func handleVMImagesImport(ctx context.Context, args []string) error {
	flags, remaining, err := cli.ParseVMImages(append([]string{"import"}, args...))
	if err != nil {
		return err
	}
	if len(remaining) != 3 || remaining[0] != "import" {
		return fmt.Errorf("vm images import requires a name and bundle directory")
	}
	name := remaining[1]
	dir := remaining[2]
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("VM image bundle directory does not exist: %s", dir)
	}
	if err != nil {
		return fmt.Errorf("inspect VM image bundle directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("VM image bundle path must be a directory: %s", dir)
	}

	pr, pw := io.Pipe()
	go func() {
		err := copyutil.TarDirectory(pw, dir, "")
		_ = pw.CloseWithError(err)
	}()

	remoteArgs := []string{"vm", "images", "import", name, "--stdin"}
	if flags.AllowLocalKernel {
		remoteArgs = append(remoteArgs, "--allow-local-kernel")
	}
	if flags.Format != "" && flags.Format != "table" {
		remoteArgs = append(remoteArgs, "--format="+flags.Format)
	}
	return execRemoteFn(ctx, systemServiceName, remoteArgs, pr, false)
}
```

Add `github.com/yeetrun/yeet/pkg/copyutil` to the imports.

- [ ] **Step 4: Run client tests to verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleSvcVMImagesImport' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add pkg/yeet/svc_cmd.go pkg/yeet/vm_images_import_test.go
mise exec -- git commit -m "pkg/yeet: upload local VM image bundles"
```

Expected: commit succeeds.

---

### Task 3: Catch Local Image Import Registry

**Files:**
- Create: `pkg/catch/vm_images_local.go`
- Create: `pkg/catch/vm_images_local_test.go`
- Modify: `pkg/catch/vm_images_cmd.go`

- [ ] **Step 1: Write failing local image registry tests**

Create `pkg/catch/vm_images_local_test.go`:

```go
package catch

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLocalVMImageName(t *testing.T) {
	valid := []string{"foo", "foo/bar", "team/ubuntu-fast", "a/b-c_d.1"}
	for _, name := range valid {
		t.Run("valid "+name, func(t *testing.T) {
			if err := validateLocalVMImageName(name); err != nil {
				t.Fatalf("validateLocalVMImageName(%q): %v", name, err)
			}
		})
	}
	invalid := []string{"", "ubuntu/26.04", "../foo", "/foo", "foo//bar", "foo/bar/", "foo bar", "Foo/Bar"}
	for _, name := range invalid {
		t.Run("invalid "+name, func(t *testing.T) {
			if err := validateLocalVMImageName(name); err == nil {
				t.Fatalf("validateLocalVMImageName(%q) returned nil error", name)
			}
		})
	}
}

func TestImportLocalVMImageRootFSOnlyUsesManagedKernel(t *testing.T) {
	root := t.TempDir()
	defaultAsset := fakeManagedVMImageAsset(t)
	var ensured bool
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			ensured = true
			return defaultAsset, nil
		},
	}

	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("rootfs"),
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !ensured {
		t.Fatal("managed asset was not ensured")
	}
	if ref.Name != "foo/bar" || ref.Payload != "vm://foo/bar" {
		t.Fatalf("ref identity = %#v", ref)
	}
	if ref.KernelPolicy != "yeet-managed" {
		t.Fatalf("kernel policy = %q, want yeet-managed", ref.KernelPolicy)
	}
	assertLocalImageFileContains(t, ref.Root, "rootfs.ext4", "rootfs")
	assertLocalImageFileContains(t, ref.Root, "vmlinux", "managed-kernel")
	assertLocalImageFileContains(t, ref.Root, "firecracker", "managed-firecracker")
	assertLocalImageFileContains(t, ref.Root, "manifest.json", ref.Version)
}

func TestImportLocalVMImageRejectsLocalKernelWithoutFlag(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	_, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("rootfs"),
			"vmlinux":    []byte("local-kernel"),
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "--allow-local-kernel") {
		t.Fatalf("error = %v, want allow local kernel error", err)
	}
}

func TestImportLocalVMImageAllowsLocalKernelWithFlag(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:             "kernel/test",
		AllowLocalKernel: true,
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("rootfs"),
			"vmlinux":    []byte("local-kernel"),
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if ref.KernelPolicy != "local" {
		t.Fatalf("kernel policy = %q, want local", ref.KernelPolicy)
	}
	assertLocalImageFileContains(t, ref.Root, "vmlinux", "local-kernel")
}

func TestResolveLocalVMImageAsset(t *testing.T) {
	root := t.TempDir()
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("rootfs"),
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	asset, err := resolveLocalVMImageAsset(context.Background(), root, "foo/bar")
	if err != nil {
		t.Fatalf("resolveLocalVMImageAsset: %v", err)
	}
	if asset.Manifest.Version != ref.Version {
		t.Fatalf("version = %q, want %q", asset.Manifest.Version, ref.Version)
	}
	if asset.Paths.Dir != ref.Root {
		t.Fatalf("dir = %q, want %q", asset.Paths.Dir, ref.Root)
	}
}

func TestRemoveLocalVMImageDeletesRefAndUnreferencedBlob(t *testing.T) {
	root := t.TempDir()
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("rootfs"),
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := removeLocalVMImage(root, "foo/bar"); err != nil {
		t.Fatalf("removeLocalVMImage: %v", err)
	}
	if _, err := os.Stat(localVMImageRefPath(root, "foo/bar")); !os.IsNotExist(err) {
		t.Fatalf("ref stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(ref.Root); !os.IsNotExist(err) {
		t.Fatalf("blob stat err = %v, want not exist", err)
	}
}

func fakeManagedVMImageAsset(t *testing.T) vmImageAsset {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "vmlinux"), "managed-kernel", 0o644)
	writeFile(t, filepath.Join(dir, "firecracker"), "managed-firecracker", 0o755)
	writeFile(t, filepath.Join(dir, "rootfs.ext4"), "managed-rootfs", 0o644)
	manifest := vmImageManifest{
		Name:         "yeet-ubuntu-26.04",
		Version:      defaultVMImageVersion,
		Architecture: "x86_64",
		Kernel:       "vmlinux",
		RootFS:       "rootfs.ext4",
		Firecracker:  "firecracker",
		RootFSSize:   int64(len("managed-rootfs")),
		Checksums:    map[string]string{},
	}
	return vmImageAsset{
		Paths: vmImagePaths{
			Dir:             dir,
			KernelPath:      filepath.Join(dir, "vmlinux"),
			RootFSPath:      filepath.Join(dir, "rootfs.ext4"),
			FirecrackerPath: filepath.Join(dir, "firecracker"),
		},
		PreparedRootFSPath: filepath.Join(dir, "rootfs.ext4"),
		Manifest:           manifest,
	}
}

func localVMImageBundleTar(t *testing.T, files map[string][]byte) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func assertLocalImageFileContains(t *testing.T, root, name, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if !strings.Contains(string(raw), want) {
		t.Fatalf("%s = %q, want containing %q", name, string(raw), want)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func decodeLocalRef(t *testing.T, path string) localVMImageRef {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	var ref localVMImageRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatalf("decode ref: %v", err)
	}
	return ref
}
```

- [ ] **Step 2: Run local image tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'Test.*LocalVMImage|TestValidateLocalVMImageName' -count=1
```

Expected: FAIL because local image registry types and functions do not exist.

- [ ] **Step 3: Implement local image registry**

Create `pkg/catch/vm_images_local.go` with:

```go
package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/copyutil"
)

const (
	localVMImageKernelPolicyManaged = "yeet-managed"
	localVMImageKernelPolicyLocal   = "local"
)

var localVMImageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

type localVMImageImportRequest struct {
	Name             string
	Reader           io.Reader
	AllowLocalKernel bool
}

type localVMImageImporter struct {
	CacheRoot          string
	EnsureManagedAsset func(context.Context) (vmImageAsset, error)
	Now                func() time.Time
}

type localVMImageRef struct {
	Name         string `json:"name"`
	Payload      string `json:"payload"`
	Version      string `json:"version"`
	ContentID    string `json:"contentID"`
	Root         string `json:"root"`
	RootFS       string `json:"rootfs"`
	Kernel       string `json:"kernel"`
	Firecracker  string `json:"firecracker"`
	KernelPolicy string `json:"kernelPolicy"`
	CreatedAt    string `json:"createdAt"`
}
```

Implement these functions:

```go
func validateLocalVMImageName(name string) error
func (i localVMImageImporter) Import(ctx context.Context, req localVMImageImportRequest) (localVMImageRef, error)
func resolveLocalVMImageAsset(ctx context.Context, cacheRoot, name string) (vmImageAsset, error)
func removeLocalVMImage(cacheRoot, name string) error
func listLocalVMImages(cacheRoot string) ([]localVMImageRef, error)
func localVMImageRefPath(cacheRoot, name string) string
```

Add this extraction helper. `copyutil.ExtractTar` already rejects absolute paths and parent traversal; the post-extract walk rejects device nodes, FIFOs, sockets, and other unsupported entries before any files are copied into the content-addressed blob:

```go
func extractLocalVMImageBundle(r io.Reader, dest string) error {
	if err := copyutil.ExtractTar(r, dest); err != nil {
		return err
	}
	return filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode.IsRegular() || mode.IsDir() || mode.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(dest, path)
		if relErr != nil {
			rel = path
		}
		return fmt.Errorf("unsupported VM image bundle entry %q", filepath.ToSlash(rel))
	})
}
```

Implementation details:

- `validateLocalVMImageName` uses `localVMImageNamePattern` and rejects `strings.HasPrefix(name, "ubuntu/")`.
- `Import` requires `req.Reader != nil`, validates the name, ensures a managed asset, extracts the tar into a temp staging directory under `os.MkdirTemp("", "yeet-local-vm-image-*")` using `extractLocalVMImageBundle`, and deletes that temp directory with `defer os.RemoveAll(stagingDir)`.
- `Import` accepts exactly one rootfs artifact named `rootfs.ext4`, `rootfs.ext4.zst`, or `rootfs.ext4.zstd`.
- `Import` rejects a supplied `vmlinux` unless `AllowLocalKernel` is true.
- `Import` always copies managed Firecracker from `managed.Paths.FirecrackerPath`.
- `Import` copies managed `vmlinux` when local kernel is not supplied.
- `Import` calculates a content ID from the bytes of rootfs, kernel, and Firecracker plus the image name. Use `sha256.New`, write the name plus NUL separators, and copy file bytes in stable order.
- `Import` writes canonical content to `<cacheRoot>/local/blobs/<contentID>`.
- `Import` writes `manifest.json` with safe version `local-<name-with-slashes-as-dashes>-<first12(contentID)>`, `Name: "yeet-local-"+name`, `Architecture: "x86_64"`, `ImageProfile: "local"`, `KernelPolicy`, `Kernel: "vmlinux"`, `RootFS`, `Firecracker: "firecracker"`, `RootFSSize` from the imported rootfs file size, and checksums for all three artifacts.
- `Import` writes `checksums.txt` in the content dir.
- `Import` writes the ref JSON atomically to `localVMImageRefPath(cacheRoot, name)`.
- `resolveLocalVMImageAsset` reads the ref, reads and validates `manifest.json`, builds `vmImagePaths`, chmods Firecracker to `0o755`, prepares compressed rootfs with `prepareVMRootFSFunc`, and returns `vmImageAsset`.
- `removeLocalVMImage` removes the ref and then removes the blob directory only when no other ref points to the same `ContentID`.

- [ ] **Step 4: Run registry tests to verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'Test.*LocalVMImage|TestValidateLocalVMImageName' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add pkg/catch/vm_images_local.go pkg/catch/vm_images_local_test.go
mise exec -- git commit -m "pkg/catch: add local VM image registry"
```

Expected: commit succeeds.

---

### Task 4: Catch VM Images Commands

**Files:**
- Modify: `pkg/catch/vm_images_cmd.go`
- Modify: `pkg/catch/vm_images_cmd_test.go`

- [ ] **Step 1: Write failing command tests**

Add to `pkg/catch/vm_images_cmd_test.go`:

```go
func TestVMImagesCmdImportReadsStdinAndPrintsRef(t *testing.T) {
	server := newTestServer(t)
	restoreManaged := stubManagedVMImageAsset(t, fakeManagedVMImageAsset(t))
	defer restoreManaged()

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: readWriter{
			Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
			Writer: &out,
		},
	}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table", Stdin: true}, []string{"import", "foo/bar"}); err != nil {
		t.Fatalf("vmImagesCmdFunc import: %v", err)
	}
	got := out.String()
	for _, want := range []string{"vm://foo/bar", "imported", "local-foo-bar-"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdImportRejectsWithoutStdin(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{}, []string{"import", "foo/bar"})
	if err == nil || !strings.Contains(err.Error(), "use yeet vm images import from the client") {
		t.Fatalf("error = %v, want client import hint", err)
	}
}

func TestVMImagesCmdListShowsLocalImages(t *testing.T) {
	server := newTestServer(t)
	restoreManaged := stubManagedVMImageAsset(t, fakeManagedVMImageAsset(t))
	defer restoreManaged()
	importer := localVMImageImporter{
		CacheRoot: server.vmImageCache().Root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	if _, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	restoreInspect := stubVMImageInspect(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: defaultVMImageVersion,
		LatestVersion: defaultVMImageVersion,
		State:         vmImageCacheCurrent,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion),
		ManifestURL:   defaultVMImageManifestURL,
	})
	defer restoreInspect()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"ls"}); err != nil {
		t.Fatalf("vmImagesCmdFunc ls: %v", err)
	}
	got := out.String()
	for _, want := range []string{"vm://ubuntu/26.04", "vm://foo/bar", "local"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdRemoveRequiresYes(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{}, []string{"rm", "foo/bar"})
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatalf("error = %v, want --yes error", err)
	}
}

func TestVMImagesCmdRemoveDeletesLocalImage(t *testing.T) {
	server := newTestServer(t)
	importer := localVMImageImporter{
		CacheRoot: server.vmImageCache().Root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	if _, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Yes: true, Format: "json"}, []string{"rm", "foo/bar"}); err != nil {
		t.Fatalf("vmImagesCmdFunc rm: %v", err)
	}
	if _, err := resolveLocalVMImageAsset(context.Background(), server.vmImageCache().Root, "foo/bar"); err == nil {
		t.Fatal("resolveLocalVMImageAsset succeeded after remove")
	}
}
```

Add helper:

```go
func stubManagedVMImageAsset(t *testing.T, asset vmImageAsset) func() {
	t.Helper()
	old := vmImageEnsureFunc
	vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		return asset, nil
	}
	return func() { vmImageEnsureFunc = old }
}
```

- [ ] **Step 2: Run command tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMImagesCmd' -count=1
```

Expected: FAIL because import/list/remove dispatch is not implemented.

- [ ] **Step 3: Implement command dispatch and rendering**

In `pkg/catch/vm_images_cmd.go`:

- Change `vmImagesUsage` to:

```go
const vmImagesUsage = "usage: yeet vm images [ls|update|import <name>|rm <name>]"
```

- Change dispatch:

```go
func (e *ttyExecer) vmImagesCmdFunc(flags cli.VMImagesFlags, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	switch {
	case len(args) == 1 && args[0] == "ls":
		return e.vmImagesListCmdFunc(flags)
	case len(args) == 1 && args[0] == "update":
		return e.vmImagesUpdateCmdFunc(flags)
	case len(args) == 2 && args[0] == "import":
		return e.vmImagesImportCmdFunc(flags, args[1])
	case len(args) == 2 && args[0] == "rm":
		return e.vmImagesRemoveCmdFunc(flags, args[1])
	default:
		return fmt.Errorf("%s", vmImagesUsage)
	}
}
```

- Implement:

```go
func (e *ttyExecer) vmImagesListCmdFunc(flags cli.VMImagesFlags) error
func (e *ttyExecer) vmImagesImportCmdFunc(flags cli.VMImagesFlags, name string) error
func (e *ttyExecer) vmImagesRemoveCmdFunc(flags cli.VMImagesFlags, name string) error
```

`vmImagesImportCmdFunc` must require `flags.Stdin`, call `localVMImageImporter{CacheRoot: e.vmImageCache().Root, EnsureManagedAsset: func(ctx context.Context) (vmImageAsset, error) { return vmImageEnsureFunc(ctx, e.vmImageCache(), vmUbuntu2604Payload, e.vmImagesProgressUI(flags)) } }.Import(e.vmImagesContext(), localVMImageImportRequest{Name: name, Reader: e.rw, AllowLocalKernel: flags.AllowLocalKernel})`, and render one row with state `imported`.

`vmImagesRemoveCmdFunc` must require `flags.Yes`, call `removeLocalVMImage`, and render one row with state `removed`.

`vmImagesListCmdFunc` must render:

- the built-in `vm://ubuntu/26.04` cache state using existing `vmImageInspectFunc`;
- every local ref from `listLocalVMImages`.

For JSON format, return an array of rows:

```go
type vmImageListRow struct {
	Payload      string `json:"payload"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Version      string `json:"version,omitempty"`
	CachePath    string `json:"cachePath,omitempty"`
	KernelPolicy string `json:"kernelPolicy,omitempty"`
}
```

For table format, columns are:

```text
PAYLOAD  KIND  STATE  VERSION  CACHE
```

- [ ] **Step 4: Run command tests to verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMImagesCmd|TestVMCmdFuncRoutesImages' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add pkg/catch/vm_images_cmd.go pkg/catch/vm_images_cmd_test.go
mise exec -- git commit -m "pkg/catch: manage local VM image refs"
```

Expected: commit succeeds.

---

### Task 5: Resolve `vm://foo/bar` During VM Run

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write failing resolver tests**

In `pkg/catch/vm_image_test.go`, replace the unsupported payload expectation in `TestResolveVMImagePayload` with source-aware tests:

```go
func TestResolveVMImagePayloadBuiltIn(t *testing.T) {
	source, err := resolveVMImagePayload(vmUbuntu2604Payload)
	if err != nil {
		t.Fatalf("resolveVMImagePayload: %v", err)
	}
	if source.Kind != vmImageSourceRemote || source.ManifestURL != defaultVMImageManifestURL {
		t.Fatalf("source = %#v, want built-in remote", source)
	}
}

func TestResolveVMImagePayloadLocal(t *testing.T) {
	source, err := resolveVMImagePayload("vm://foo/bar")
	if err != nil {
		t.Fatalf("resolveVMImagePayload local: %v", err)
	}
	if source.Kind != vmImageSourceLocal || source.LocalName != "foo/bar" {
		t.Fatalf("source = %#v, want local foo/bar", source)
	}
}

func TestResolveVMImagePayloadRejectsInvalidLocalName(t *testing.T) {
	_, err := resolveVMImagePayload("vm://Foo/Bar")
	if err == nil || !strings.Contains(err.Error(), "invalid local VM image name") {
		t.Fatalf("error = %v, want invalid local image name", err)
	}
}
```

Add:

```go
func TestEnsureVMImageAssetWithProgressUsesLocalRef(t *testing.T) {
	root := t.TempDir()
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	asset, err := ensureVMImageAssetWithProgress(context.Background(), vmImageCache{Root: root}, "vm://foo/bar", nil)
	if err != nil {
		t.Fatalf("ensureVMImageAssetWithProgress: %v", err)
	}
	if asset.Manifest.Version != ref.Version {
		t.Fatalf("version = %q, want %q", asset.Manifest.Version, ref.Version)
	}
}

func TestEnsureVMImageAssetWithProgressReportsUnknownLocalRef(t *testing.T) {
	_, err := ensureVMImageAssetWithProgress(context.Background(), vmImageCache{Root: t.TempDir()}, "vm://foo/bar", nil)
	if err == nil || !strings.Contains(err.Error(), "import it with `yeet vm images import foo/bar") {
		t.Fatalf("error = %v, want import hint", err)
	}
}
```

- [ ] **Step 2: Run resolver tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestResolveVMImagePayload|TestEnsureVMImageAssetWithProgress' -count=1
```

Expected: FAIL because resolver still returns a manifest URL string and rejects `vm://foo/bar`.

- [ ] **Step 3: Refactor resolver to source-aware resolution**

In `pkg/catch/vm_image.go`, add:

```go
type vmImageSourceKind string

const (
	vmImageSourceRemote vmImageSourceKind = "remote"
	vmImageSourceLocal  vmImageSourceKind = "local"
)

type vmImageSource struct {
	Kind        vmImageSourceKind
	ManifestURL string
	LocalName   string
}
```

Change `resolveVMImagePayload(payload string) (vmImageSource, error)`:

```go
func resolveVMImagePayload(payload string) (vmImageSource, error) {
	payload = strings.TrimSpace(payload)
	switch payload {
	case vmUbuntu2604Payload:
		return vmImageSource{Kind: vmImageSourceRemote, ManifestURL: defaultVMImageManifestURL}, nil
	case "":
		return vmImageSource{}, fmt.Errorf("VM image payload is required")
	default:
		const prefix = "vm://"
		if strings.HasPrefix(payload, prefix) {
			name := strings.TrimPrefix(payload, prefix)
			if err := validateLocalVMImageName(name); err != nil {
				return vmImageSource{}, fmt.Errorf("invalid local VM image name %q: %w", name, err)
			}
			return vmImageSource{Kind: vmImageSourceLocal, LocalName: name}, nil
		}
		return vmImageSource{}, fmt.Errorf("unsupported VM image payload %q (supported: %s or imported vm://<name>)", payload, vmUbuntu2604Payload)
	}
}
```

Update `ensureVMImageAssetWithProgress`:

```go
source, err := resolveVMImagePayload(payload)
if err != nil { return vmImageAsset{}, err }
if source.Kind == vmImageSourceLocal {
	return resolveLocalVMImageAsset(ctx, cache.Root, source.LocalName)
}
cache = cache.withManifestURL(source.ManifestURL)
```

Update `vmImageCache.Inspect` to reject local payloads with:

```go
if source.Kind == vmImageSourceLocal {
	asset, err := resolveLocalVMImageAsset(ctx, c.Root, source.LocalName)
	if err != nil { ... }
	state := vmImageCacheState{Payload: payload, CachedVersion: asset.Manifest.Version, LatestVersion: asset.Manifest.Version, State: vmImageCacheCurrent, CachePath: asset.Paths.Dir}
	return state, asset.Manifest, nil
}
```

Keep built-in update behavior unchanged.

- [ ] **Step 4: Add runVM provision test**

In `pkg/catch/vm_provision_test.go`, add:

```go
func TestRunVMLocalImportedImage(t *testing.T) {
	server := newTestServer(t)
	service := "svc"
	execer, _, _, _ := newVMProvisionTestExecer(t, server, service)
	importer := localVMImageImporter{
		CacheRoot: filepath.Join(server.cfg.RootDir, "vm-images"),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := execer.runVM(cli.RunFlags{Net: "svc"}, "vm://foo/bar"); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	assertVMImageVersion(t, server, service, ref.Version)
}
```

- [ ] **Step 5: Run resolver/provision tests to verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestResolveVMImagePayload|TestEnsureVMImageAssetWithProgress|TestRunVMLocalImportedImage|TestRunVMRejectsUnsupportedImage' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
git add pkg/catch/vm_image.go pkg/catch/vm_image_test.go pkg/catch/vm_provision_test.go
mise exec -- git commit -m "pkg/catch: resolve imported VM images"
```

Expected: commit succeeds.

---

### Task 6: Docs And CLI Help

**Files:**
- Modify: `README.md`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`

- [ ] **Step 1: Update README**

Add this section after the standard VM run examples:

```markdown
### Local VM images

Import a local rootfs bundle onto a catch host, then run it by name:

```bash
yeet vm images import lab/ubuntu ./dist/my-vm
yeet run devbox vm://lab/ubuntu
yeet vm images rm lab/ubuntu --yes
```

A local bundle is a directory containing `rootfs.ext4` or `rootfs.ext4.zst`.
Rootfs-only bundles use yeet's managed kernel and Firecracker from the current
Ubuntu VM image. To test a local kernel, include `vmlinux` in the bundle and
import with `--allow-local-kernel`:

```bash
yeet vm images import kernel/test ./dist/my-vm --allow-local-kernel
```

Imported image names are catch-host global. Re-importing the same name updates
the ref for new VMs; existing VMs keep the exact imported content version they
were created with.
```
```

- [ ] **Step 2: Update website VM docs**

Add the same workflow to `website/docs/payloads/vms.mdx` under a heading:

```mdx
## Local VM images
```

Use generic names `lab/ubuntu` and `kernel/test`. Do not include private hostnames.

- [ ] **Step 3: Update CLI docs**

In `website/docs/cli/yeet-cli.mdx`, update the `yeet vm images` section to include:

```bash
yeet vm images
yeet vm images ls
yeet vm images update
yeet vm images import <name> <bundle-dir>
yeet vm images import <name> <bundle-dir> --allow-local-kernel
yeet vm images rm <name> --yes
```

- [ ] **Step 4: Run docs checks**

Run:

```bash
git -C website diff --check
rg -n "actual-private-host|actual-local-user" README.md website/docs
```

Expected: `git diff --check` exits 0. `rg` prints no matches.

- [ ] **Step 5: Commit website docs**

Run:

```bash
git -C website add docs/payloads/vms.mdx docs/cli/yeet-cli.mdx
git -C website commit -m "docs: document local VM image imports"
```

Expected: commit succeeds.

- [ ] **Step 6: Commit root docs and submodule pointer**

Run:

```bash
git add README.md website
mise exec -- git commit -m "docs: document local VM image imports"
```

Expected: commit succeeds.

---

### Task 7: Full Verification And Live E2E

**Files:**
- No source edits expected.

- [ ] **Step 1: Run targeted tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch ./pkg/copyutil -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Redeploy catch to a live VM test host**

Run:

```bash
mise exec -- go run ./cmd/yeet init root@<vm-test-host>
```

Expected: catch installs successfully.

- [ ] **Step 5: Build local yeet binary**

Run:

```bash
mise exec -- go build -o /tmp/yeet-local-vm-images ./cmd/yeet
```

Expected: binary builds successfully.

- [ ] **Step 6: Create a disposable local bundle from the managed cached image on the live test host**

Run:

```bash
ssh root@<vm-test-host> 'bash -lc '\''
set -euo pipefail
src="$(find /root/data/vm-images -maxdepth 2 -name rootfs.ext4 -type f | sort | tail -1)"
test -n "$src"
rm -rf /tmp/yeet-local-vm-bundle
mkdir -p /tmp/yeet-local-vm-bundle
cp "$src" /tmp/yeet-local-vm-bundle/rootfs.ext4
ls -lh /tmp/yeet-local-vm-bundle/rootfs.ext4
'\'''
mkdir -p /tmp/yeet-local-vm-bundle
scp root@<vm-test-host>:/tmp/yeet-local-vm-bundle/rootfs.ext4 /tmp/yeet-local-vm-bundle/rootfs.ext4
```

Expected: local `/tmp/yeet-local-vm-bundle/rootfs.ext4` exists. This uses the live test host only as a source of an already compatible rootfs; the import path itself still uploads from the local client.

- [ ] **Step 7: Import and list the local image**

Run:

```bash
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images vm images import local/test /tmp/yeet-local-vm-bundle
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images vm images ls
```

Expected: import output shows `vm://local/test`, state `imported`, and a `local-local-test-<hash>` version. List output shows both `vm://ubuntu/26.04` and `vm://local/test`.

- [ ] **Step 8: Run a VM from the imported local image**

Run:

```bash
tmpdir="$(mktemp -d /tmp/yeet-local-vm-run-XXXXXX)"
cd "$tmpdir"
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images run vmlocal0605 vm://local/test --disk=8g --net=svc
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images ssh vmlocal0605 -- hostname
```

Expected: deploy reaches `VM vmlocal0605 is running.` SSH prints `vmlocal0605`.

- [ ] **Step 9: Remove VM and imported image**

Run:

```bash
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images rm vmlocal0605 --yes --clean-data --clean-config
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images vm images rm local/test --yes
CATCH_HOST=<catch-host-alias> /tmp/yeet-local-vm-images vm images ls
```

Expected: `vmlocal0605` is removed, `vm://local/test` no longer appears in the image list, and no matching systemd unit or service root remains on the live test host.

- [ ] **Step 10: Commit any final fixes**

If live testing required source changes, run the targeted and full verification commands again, then commit:

```bash
git add <changed-files>
mise exec -- git commit -m "test: harden local VM image import"
```

Expected: commit succeeds only if there were source changes after live testing.

---

## Self Review

- Spec coverage: The plan covers `yeet vm images import`, named image refs, `vm://foo/bar` resolution, cleanup with `rm`, catch-host-global storage, rootfs-only imports using yeet-managed kernel and Firecracker, explicit `--allow-local-kernel`, Mac-friendly client upload over RPC stdin, docs, and live host validation.
- Placeholder scan: The plan has no `TODO`, `TBD`, or unspecified implementation steps. Every task includes exact files, test names, commands, and expected outcomes.
- Type consistency: The same names are used throughout: `VMImagesFlags.AllowLocalKernel`, `VMImagesFlags.Stdin`, `VMImagesFlags.Yes`, `localVMImageRef`, `localVMImageImporter`, `localVMImageImportRequest`, `validateLocalVMImageName`, `resolveLocalVMImageAsset`, and `removeLocalVMImage`.
