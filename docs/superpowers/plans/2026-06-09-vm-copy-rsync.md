# VM Copy Rsync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet copy` copy into and out of VM guest filesystems with rsync over the same SSH transport used by `yeet ssh`.

**Architecture:** Parse copy endpoints without prematurely rejecting VM absolute paths, then inspect the remote service type through `ServiceInfo`. Regular services continue through the existing catch-side service-data tar stream after service-data path normalization. VM services use a shared VM SSH execution plan to build a local `rsync -avz -e <ssh>` command, including proxy/direct behavior, yeet VM known-hosts, stale-key repair, and `--force-proxy`.

**Tech Stack:** Go, `pkg/yeet` client orchestration, `pkg/cli` command metadata, catch RPC `ServiceInfo`, local `ssh` and `rsync`, website MDX docs, yeet-vm-images Bash/Nix image builders, lab-host live VM validation.

---

## File Structure

- Modify `pkg/yeet/copy_cmd.go`: copy parsing, service-type routing, service-data path normalization, VM rsync execution, rsync error hints.
- Modify `pkg/yeet/copy_cmd_test.go`: parser, routing, regular service preservation, VM rsync argument, dependency, and error tests.
- Modify `pkg/yeet/ssh_cmd.go`: expose a small shared VM SSH execution-plan helper usable by both `yeet ssh` and VM copy.
- Modify `pkg/yeet/ssh_cmd_test.go`: regression tests for the shared helper and unchanged SSH behavior.
- Modify `pkg/cli/cli.go`: copy command description, usage, and examples.
- Modify `cmd/yeet/cli_test.go`: help-output assertions for new copy syntax.
- Modify `website/docs/cli/yeet-cli.mdx`: document service-vs-VM copy behavior and `--force-proxy`.
- Modify `website/docs/payloads/vms.mdx`: add VM guest copy examples.
- Regenerate `.codex/skills/yeet-cli/references/yeet-help-llm.md` with `tools/generate-yeet-help-llm.sh`.
- Modify `tools/vm-image/build-ubuntu-26.04.sh`, `tools/vm-image/build_ubuntu_test.go`, and `tools/vm-image/README.md`: keep the in-repo Ubuntu image builder aligned with official image policy and rsync availability.
- Modify `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`, `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`, and `/Users/shayne/code/yeet-vm-images/README.md`: add rsync and bump Ubuntu image version.
- Modify `/Users/shayne/code/yeet-vm-images/nixos/yeet-vm.nix`, `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`, `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`, `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`, and `/Users/shayne/code/yeet-vm-images/README.md`: add rsync and bump NixOS image version.
- Modify `pkg/catch/vm_image_registry.go`, `pkg/catch/vm_image_test.go`, and `pkg/catch/vm_images_cmd_test.go`: point built-in defaults/tests at the new Ubuntu image version after publishing.
- Modify `website/docs/changelog.mdx`: add patch release notes after implementation and validation.

## Task 1: Copy Parser Preserves VM Paths And Parses Force Proxy

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing parser tests**

Add these cases to `TestParseCopyArgs` in `pkg/yeet/copy_cmd_test.go`.

```go
{
	name: "vm absolute remote destination path is preserved",
	args: []string{"local.txt", "devbox:/etc/nginx/nginx.conf"},
	want: copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
		Src:       copyEndpoint{Raw: "local.txt", Path: "local.txt"},
		Dst:       copyEndpoint{Raw: "devbox:/etc/nginx/nginx.conf", Path: "/etc/nginx/nginx.conf", Service: "devbox", Remote: true},
	},
},
{
	name: "vm tilde remote destination path is preserved",
	args: []string{"local.txt", "devbox:~/app/config.yml"},
	want: copyRequest{
		Recursive: true,
		Archive:   true,
		Compress:  true,
		Verbose:   true,
		Src:       copyEndpoint{Raw: "local.txt", Path: "local.txt"},
		Dst:       copyEndpoint{Raw: "devbox:~/app/config.yml", Path: "~/app/config.yml", Service: "devbox", Remote: true},
	},
},
{
	name: "force proxy flag is consumed by copy",
	args: []string{"--force-proxy", "local.txt", "devbox:~/config.yml"},
	want: copyRequest{
		Recursive:  true,
		Archive:    true,
		Compress:   true,
		Verbose:    true,
		ForceProxy: true,
		Src:        copyEndpoint{Raw: "local.txt", Path: "local.txt"},
		Dst:        copyEndpoint{Raw: "devbox:~/config.yml", Path: "~/config.yml", Service: "devbox", Remote: true},
	},
},
{
	name:    "force proxy value is rejected",
	args:    []string{"--force-proxy=true", "local.txt", "devbox:~/config.yml"},
	wantErr: "copy --force-proxy does not take a value",
},
```

Update the existing `"remote destination with bundled flags"` expectation so `Path` remains the raw remote path and `DirHint` is still true:

```go
Dst: copyEndpoint{Raw: "svc:data/logs/", Path: "data/logs/", Service: "svc", Remote: true, DirHint: true},
```

Update the existing `"double dash keeps dash path operand"` expectation so `Path` stays `"."`:

```go
Dst: copyEndpoint{Raw: "svc:.", Path: ".", Service: "svc", Remote: true, DirHint: true},
```

- [ ] **Step 2: Run parser tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestParseCopyArgs|TestApplyLongCopyFlag' -count=1
```

Expected: fails because absolute remote paths are still rejected and `--force-proxy` is unknown.

- [ ] **Step 3: Implement raw remote path parsing and `--force-proxy`**

In `pkg/yeet/copy_cmd.go`, add `ForceProxy` to `copyRequest`:

```go
type copyRequest struct {
	Recursive  bool
	Archive    bool
	Compress   bool
	Verbose    bool
	ForceProxy bool
	Src        copyEndpoint
	Dst        copyEndpoint
}
```

Extend `applyLongCopyFlag`:

```go
func applyLongCopyFlag(req *copyRequest, arg string) error {
	switch {
	case arg == "--recursive":
		req.Recursive = true
	case arg == "--archive":
		req.Archive = true
		req.Recursive = true
	case arg == "--compress":
		req.Compress = true
	case arg == "--verbose":
		req.Verbose = true
	case arg == "--force-proxy":
		req.ForceProxy = true
	case strings.HasPrefix(arg, "--force-proxy="):
		return fmt.Errorf("copy --force-proxy does not take a value")
	default:
		return fmt.Errorf("unknown flag")
	}
	return nil
}
```

Replace the remote-path normalization inside `parseCopyEndpoint` with raw path preservation:

```go
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
	remotePath := strings.TrimSpace(raw[idx+1:])
	return copyEndpoint{
		Raw:     raw,
		Path:    remotePath,
		Service: service,
		Host:    host,
		Remote:  true,
		DirHint: remotePathDirHint(remotePath),
	}, nil
}

func remotePathDirHint(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed == "" || trimmed == "." || trimmed == "./" || strings.HasSuffix(trimmed, "/")
}
```

- [ ] **Step 4: Run parser tests and verify pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestParseCopyArgs|TestApplyLongCopyFlag|FuzzYeetStringNormalizers' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit parser change**

```bash
git add pkg/yeet/copy_cmd.go pkg/yeet/copy_cmd_test.go
git commit -m "copy: preserve VM remote paths"
```

## Task 2: Keep Regular Service Copy Rooted At Data

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_test.go`

- [ ] **Step 1: Write failing service-data normalization tests**

Add this test to `pkg/yeet/copy_cmd_test.go`:

```go
func TestNormalizeServiceDataCopyRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     copyRequest
		want    copyRequest
		wantErr string
	}{
		{
			name: "upload strips data prefix",
			req: copyRequest{
				Src: copyEndpoint{Raw: "local.txt", Path: "local.txt"},
				Dst: copyEndpoint{Raw: "svc:data/logs/", Path: "data/logs/", Service: "svc", Remote: true, DirHint: true},
			},
			want: copyRequest{
				Src: copyEndpoint{Raw: "local.txt", Path: "local.txt"},
				Dst: copyEndpoint{Raw: "svc:data/logs/", Path: "logs", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{
			name: "download dot targets data root",
			req: copyRequest{
				Src: copyEndpoint{Raw: "svc:.", Path: ".", Service: "svc", Remote: true, DirHint: true},
				Dst: copyEndpoint{Raw: "./out", Path: "./out"},
			},
			want: copyRequest{
				Src: copyEndpoint{Raw: "svc:.", Path: "", Service: "svc", Remote: true, DirHint: true},
				Dst: copyEndpoint{Raw: "./out", Path: "./out"},
			},
		},
		{
			name: "regular service rejects absolute destination",
			req: copyRequest{
				Src: copyEndpoint{Raw: "local.txt", Path: "local.txt"},
				Dst: copyEndpoint{Raw: "svc:/etc/passwd", Path: "/etc/passwd", Service: "svc", Remote: true},
			},
			wantErr: "remote path must be relative",
		},
		{
			name: "regular service rejects absolute source",
			req: copyRequest{
				Src: copyEndpoint{Raw: "svc:/etc/passwd", Path: "/etc/passwd", Service: "svc", Remote: true},
				Dst: copyEndpoint{Raw: "./out", Path: "./out"},
			},
			wantErr: "remote path must be relative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeServiceDataCopyRequest(tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("normalizeServiceDataCopyRequest error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeServiceDataCopyRequest: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeServiceDataCopyRequest = %#v, want %#v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run normalization tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestNormalizeServiceDataCopyRequest -count=1
```

Expected: FAIL with `undefined: normalizeServiceDataCopyRequest`.

- [ ] **Step 3: Implement service-data normalization helper**

Add these helpers to `pkg/yeet/copy_cmd.go` near the endpoint helpers:

```go
func normalizeServiceDataCopyRequest(req copyRequest) (copyRequest, error) {
	if req.Src.Remote {
		src, err := normalizeServiceDataEndpoint(req.Src)
		if err != nil {
			return copyRequest{}, err
		}
		req.Src = src
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

func normalizeServiceDataEndpoint(ep copyEndpoint) (copyEndpoint, error) {
	rel, dirHint, err := normalizeRemotePath(ep.Path)
	if err != nil {
		return copyEndpoint{}, err
	}
	ep.Path = rel
	ep.DirHint = ep.DirHint || dirHint
	return ep, nil
}
```

Rename the current service-data copy functions so their responsibility is explicit:

```go
func copyServiceDataToRemote(req copyRequest) error {
	// body of current copyToRemote
}

func copyServiceDataFromRemote(req copyRequest) (err error) {
	// body of current copyFromRemote
}
```

Update call sites in this file from `copyToRemote(req)` to `copyServiceDataToRemote(req)` and from `copyFromRemote(req)` to `copyServiceDataFromRemote(req)`.

- [ ] **Step 4: Route regular services through normalization**

Temporarily keep all copy endpoints on the service-data path by changing `runCopyCommand` to normalize before calling the renamed helpers:

```go
func runCopyCommand(args []string, cfg *ProjectConfig) error {
	req, err := parseCopyArgs(args)
	if err != nil {
		return err
	}
	direction, remote, err := classifyCopyEndpoints(req)
	if err != nil {
		return err
	}
	if req.ForceProxy {
		return fmt.Errorf("copy --force-proxy only applies to VM services")
	}
	if err := applyCopyHostOverrideForEndpoint(remote, cfg); err != nil {
		return err
	}
	req, err = normalizeServiceDataCopyRequest(req)
	if err != nil {
		return err
	}
	if direction == copyDirectionFromRemote {
		return copyServiceDataFromRemote(req)
	}
	return copyServiceDataToRemote(req)
}
```

Task 3 will replace this temporary all-regular routing with service-type routing.

- [ ] **Step 5: Update existing copy tests for renamed functions**

In `pkg/yeet/copy_cmd_test.go`, update any direct references to `copyToRemote` or `copyFromRemote` to the renamed `copyServiceDataToRemote` and `copyServiceDataFromRemote` names. Existing `HandleSvcCmd` copy tests should keep passing because `runCopyCommand` normalizes before sending catch args.

- [ ] **Step 6: Run regular copy tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestNormalizeServiceDataCopyRequest|TestHandleSvcCmdCopy|TestRemoteCopyCommandArgs|TestOpenPlainFileCopyUpload|TestCopyEndpointValidationHelpers' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit regular service preservation**

```bash
git add pkg/yeet/copy_cmd.go pkg/yeet/copy_cmd_test.go pkg/yeet/handle_svc_cmd_test.go
git commit -m "copy: keep service data path normalization"
```

## Task 3: Route VM Endpoints To VM Copy

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing routing tests**

Add this test to `pkg/yeet/copy_cmd_test.go`:

```go
func TestRunCopyCommandRoutesVMEndpointToRsync(t *testing.T) {
	oldServerInfo := fetchSSHServerInfoFunc
	oldServiceInfo := fetchSSHServiceInfoFunc
	oldRunVM := runVMRsyncCopyFunc
	oldExec := execRemoteFn
	oldHost := Host()
	oldOverride := hostOverride
	oldOverrideSet := hostOverrideSet
	defer func() {
		fetchSSHServerInfoFunc = oldServerInfo
		fetchSSHServiceInfoFunc = oldServiceInfo
		runVMRsyncCopyFunc = oldRunVM
		execRemoteFn = oldExec
		SetHost(oldHost)
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
	}()
	resetHostOverride()
	SetHost("yeet-lab")

	fetchSSHServerInfoFunc = func(ctx context.Context, host string) (serverInfo, error) {
		if host != "yeet-lab" {
			t.Fatalf("server info host = %q, want yeet-lab", host)
		}
		return serverInfo{InstallUser: "root"}, nil
	}
	fetchSSHServiceInfoFunc = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-lab" || service != "devbox" {
			t.Fatalf("service info = %s %s, want yeet-lab devbox", host, service)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		}, nil
	}
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error {
		t.Fatal("regular service-data copy path should not run for VM endpoint")
		return nil
	}

	var gotReq copyRequest
	var gotDirection copyDirection
	var gotRemote copyEndpoint
	runVMRsyncCopyFunc = func(ctx context.Context, req copyRequest, direction copyDirection, remote copyEndpoint, remoteCtx copyRemoteContext) error {
		gotReq = req
		gotDirection = direction
		gotRemote = remote
		if remoteCtx.Host != "yeet-lab" || remoteCtx.Server.InstallUser != "root" || remoteCtx.Service.Info.ServiceType != serviceTypeVM {
			t.Fatalf("remote context = %#v", remoteCtx)
		}
		return nil
	}

	if err := runCopyCommand([]string{"--force-proxy", "./local.txt", "devbox:/etc/motd"}, nil); err != nil {
		t.Fatalf("runCopyCommand: %v", err)
	}
	if !gotReq.ForceProxy || gotDirection != copyDirectionToRemote || gotRemote.Path != "/etc/motd" {
		t.Fatalf("VM copy routing = req %#v direction %v remote %#v", gotReq, gotDirection, gotRemote)
	}
}
```

Add this regular-service guard test:

```go
func TestRunCopyCommandRejectsForceProxyForRegularService(t *testing.T) {
	oldServerInfo := fetchSSHServerInfoFunc
	oldServiceInfo := fetchSSHServiceInfoFunc
	defer func() {
		fetchSSHServerInfoFunc = oldServerInfo
		fetchSSHServiceInfoFunc = oldServiceInfo
		resetHostOverride()
	}()
	resetHostOverride()
	SetHost("yeet-lab")

	fetchSSHServerInfoFunc = func(context.Context, string) (serverInfo, error) {
		return serverInfo{}, nil
	}
	fetchSSHServiceInfoFunc = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{ServiceType: dockerServiceType}}, nil
	}

	err := runCopyCommand([]string{"--force-proxy", "./local.txt", "web:config.yml"}, nil)
	if err == nil || !strings.Contains(err.Error(), "copy --force-proxy only applies to VM services") {
		t.Fatalf("runCopyCommand error = %v, want force proxy regular service error", err)
	}
}
```

- [ ] **Step 2: Run routing tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunCopyCommandRoutesVMEndpointToRsync|TestRunCopyCommandRejectsForceProxyForRegularService' -count=1
```

Expected: FAIL with undefined `runVMRsyncCopyFunc` and `copyRemoteContext`.

- [ ] **Step 3: Add copy remote context and routing**

In `pkg/yeet/copy_cmd.go`, add this type and package variable:

```go
type copyRemoteContext struct {
	Host    string
	Server  serverInfo
	Service catchrpc.ServiceInfoResponse
}

var runVMRsyncCopyFunc = runVMRsyncCopy
```

Add the import if missing:

```go
import "github.com/yeetrun/yeet/pkg/catchrpc"
```

Add remote context resolution:

```go
func resolveCopyRemoteContext(ctx context.Context, remote copyEndpoint, cfg *ProjectConfig) (copyRemoteContext, error) {
	if err := applyCopyHostOverrideForEndpoint(remote, cfg); err != nil {
		return copyRemoteContext{}, err
	}
	host := Host()
	server, err := fetchSSHServerInfoFunc(ctx, host)
	if err != nil {
		return copyRemoteContext{}, err
	}
	resp, err := fetchSSHServiceInfoFunc(ctx, host, remote.Service)
	if err != nil {
		return copyRemoteContext{}, err
	}
	if !resp.Found {
		msg := strings.TrimSpace(resp.Message)
		if msg == "" {
			msg = fmt.Sprintf("service %q not found", remote.Service)
		}
		return copyRemoteContext{}, errors.New(msg)
	}
	return copyRemoteContext{Host: host, Server: server, Service: resp}, nil
}
```

Ensure `errors` is imported.

Replace `runCopyCommand` with service-type routing:

```go
func runCopyCommand(args []string, cfg *ProjectConfig) error {
	req, err := parseCopyArgs(args)
	if err != nil {
		return err
	}
	direction, remote, err := classifyCopyEndpoints(req)
	if err != nil {
		return err
	}
	remoteCtx, err := resolveCopyRemoteContext(context.Background(), remote, cfg)
	if err != nil {
		return err
	}
	if remoteCtx.Service.Info.ServiceType == serviceTypeVM {
		return runVMRsyncCopyFunc(context.Background(), req, direction, remote, remoteCtx)
	}
	if req.ForceProxy {
		return fmt.Errorf("copy --force-proxy only applies to VM services")
	}
	req, err = normalizeServiceDataCopyRequest(req)
	if err != nil {
		return err
	}
	if direction == copyDirectionFromRemote {
		return copyServiceDataFromRemote(req)
	}
	return copyServiceDataToRemote(req)
}
```

Add this stub below the routing code so tests compile before Task 4:

```go
func runVMRsyncCopy(ctx context.Context, req copyRequest, direction copyDirection, remote copyEndpoint, remoteCtx copyRemoteContext) error {
	return fmt.Errorf("VM copy is not implemented")
}
```

- [ ] **Step 4: Run routing and regular copy tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunCopyCommandRoutesVMEndpointToRsync|TestRunCopyCommandRejectsForceProxyForRegularService|TestHandleSvcCmdCopy' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit VM routing**

```bash
git add pkg/yeet/copy_cmd.go pkg/yeet/copy_cmd_test.go
git commit -m "copy: route VM endpoints separately"
```

## Task 4: Share VM SSH Planning With Rsync

**Files:**
- Modify: `pkg/yeet/ssh_cmd.go`
- Modify: `pkg/yeet/ssh_cmd_test.go`

- [ ] **Step 1: Write failing shared-plan test**

Add this test to `pkg/yeet/ssh_cmd_test.go`:

```go
func TestVMSSHExecutionPlanForServiceBuildsProxyPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	plan, err := vmSSHExecutionPlanForServiceInfo(
		"yeet-lab",
		serverInfo{InstallUser: "root"},
		"devbox",
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("vmSSHExecutionPlanForServiceInfo: %v", err)
	}
	for _, want := range []string{
		"-l", "ubuntu",
		"HostName=192.168.100.12",
		"HostKeyAlias=yeet-vm-devbox@yeet-lab",
		"ProxyCommand=ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + filepath.Join(home, ".yeet", "known_hosts") + " -o HostKeyAlias=yeet-proxy@yeet-lab -o CheckHostIP=no -W %h:%p root@yeet-lab",
		"root@yeet-lab",
	} {
		if !sshOptionsContainValue(plan.Args, want) && !slices.Contains(plan.Args, want) {
			t.Fatalf("plan args = %#v, want %q", plan.Args, want)
		}
	}
	if plan.Notice != "Proxying VM SSH through yeet-lab to 192.168.100.12" {
		t.Fatalf("notice = %q", plan.Notice)
	}
	if plan.KnownHostRepair == nil || !slices.Contains(plan.KnownHostRepair.ExtraAliases, "yeet-proxy@yeet-lab") {
		t.Fatalf("repair = %#v, want proxy alias", plan.KnownHostRepair)
	}
}
```

- [ ] **Step 2: Run SSH shared-plan test and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestVMSSHExecutionPlanForServiceBuildsProxyPlan -count=1
```

Expected: FAIL with `undefined: vmSSHExecutionPlanForServiceInfo`.

- [ ] **Step 3: Add shared VM SSH execution-plan helper**

In `pkg/yeet/ssh_cmd.go`, add this helper near `serviceShellCommandPlanFromResponseWithForce`:

```go
func vmSSHExecutionPlanForServiceInfo(host string, info serverInfo, service string, resp catchrpc.ServiceInfoResponse, command []string, options []string, forceProxy bool) (sshExecutionPlan, error) {
	if !resp.Found {
		return sshExecutionPlan{}, serviceNotFoundShellError(service, resp.Message)
	}
	if resp.Info.ServiceType != serviceTypeVM {
		return sshExecutionPlan{}, fmt.Errorf("service %q is not a VM service", service)
	}
	vmPlan, err := buildVMSSHOptionsPlan(host, info, service, resp, options, forceProxy)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	inv := sshInvocation{
		Options:    vmPlan.Options,
		Service:    service,
		Command:    command,
		ForceProxy: forceProxy,
	}
	return sshExecutionPlan{
		Args:            sshArgsFromInvocation(host, info, inv),
		KnownHostRepair: vmPlan.KnownHostRepair,
		Notice:          vmPlan.Notice,
	}, nil
}
```

Then simplify `serviceShellCommandPlanFromResponseWithForce` for VMs to consume the helper:

```go
if resp.Info.ServiceType == serviceTypeVM {
	plan, err := vmSSHExecutionPlanForServiceInfo(host, info, service, resp, command, options, forceProxy)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return command, plan.Args[:len(plan.Args)-1], plan.KnownHostRepair, plan.Notice, nil
}
```

Keep the non-VM branch unchanged.

- [ ] **Step 4: Run SSH tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestVMSSHExecutionPlanForServiceBuildsProxyPlan|TestServiceShellCommandForVM|TestServiceShellCommandForVMLAN|TestServiceShellCommandForVMSvcLAN|TestSSHExecutionPlan' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit shared SSH planning**

```bash
git add pkg/yeet/ssh_cmd.go pkg/yeet/ssh_cmd_test.go
git commit -m "ssh: share VM connection planning"
```

## Task 5: Implement VM Rsync Copy

**Files:**
- Modify: `pkg/yeet/copy_cmd.go`
- Modify: `pkg/yeet/copy_cmd_test.go`

- [ ] **Step 1: Write failing rsync argument and dependency tests**

Add these package-level stubs to tests as needed:

```go
type recordedRsync struct {
	args []string
	err  error
}
```

Add this test to `pkg/yeet/copy_cmd_test.go`:

```go
func TestRunVMRsyncCopyUploadBuildsRsyncCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}

	var got recordedRsync
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		got.args = append([]string{}, args...)
		return nil
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Verbose:  true,
		Src:      copyEndpoint{Raw: "./local.txt", Path: "./local.txt"},
		Dst:      copyEndpoint{Raw: "devbox:/etc/motd", Path: "/etc/motd", Service: "devbox", Remote: true},
	}
	remote := req.Dst
	remoteCtx := copyRemoteContext{
		Host:   "yeet-lab",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
	}

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionToRemote, remote, remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	if len(got.args) == 0 {
		t.Fatal("rsync did not run")
	}
	for _, want := range []string{"-avz", "-e", "./local.txt", "root@yeet-lab:/etc/motd"} {
		if !slices.Contains(got.args, want) {
			t.Fatalf("rsync args = %#v, want %q", got.args, want)
		}
	}
	remoteShell := got.args[slices.Index(got.args, "-e")+1]
	for _, want := range []string{"ssh", "-l ubuntu", "-o HostName=192.168.100.12", "ProxyCommand=ssh"} {
		if !strings.Contains(remoteShell, want) {
			t.Fatalf("remote shell = %q, want %q", remoteShell, want)
		}
	}
}
```

Add a download test:

```go
func TestRunVMRsyncCopyDownloadBuildsRsyncCommand(t *testing.T) {
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
		Src:      copyEndpoint{Raw: "devbox:~/app.log", Path: "~/app.log", Service: "devbox", Remote: true},
		Dst:      copyEndpoint{Raw: "./logs/", Path: "./logs/"},
	}
	remoteCtx := copyRemoteContext{
		Host:   "yeet-lab",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
	}

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionFromRemote, req.Src, remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	for _, want := range []string{"-avz", "root@yeet-lab:~/app.log", "./logs/"} {
		if !slices.Contains(gotArgs, want) {
			t.Fatalf("rsync args = %#v, want %q", gotArgs, want)
		}
	}
}
```

Add missing local rsync test:

```go
func TestRunVMRsyncCopyMissingLocalRsync(t *testing.T) {
	oldLookPath := lookPathCopyBinaryFunc
	defer func() { lookPathCopyBinaryFunc = oldLookPath }()
	lookPathCopyBinaryFunc = func(name string) (string, error) {
		if name == "rsync" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + name, nil
	}

	err := runVMRsyncCopy(context.Background(), copyRequest{}, copyDirectionToRemote, copyEndpoint{Service: "devbox"}, copyRemoteContext{
		Host:   "yeet-lab",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM:          &catchrpc.ServiceVM{SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"}},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "rsync CLI not found in PATH") {
		t.Fatalf("runVMRsyncCopy error = %v, want missing rsync", err)
	}
}
```

- [ ] **Step 2: Run rsync tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunVMRsyncCopy' -count=1
```

Expected: FAIL with missing rsync runner symbols or the existing stub returning `VM copy is not implemented`.

- [ ] **Step 3: Add rsync runner hooks and dependency checks**

In `pkg/yeet/copy_cmd.go`, add package variables near the existing copy globals:

```go
type rsyncCommandRunner func(context.Context, []string, io.Writer, io.Writer) error

var (
	lookPathCopyBinaryFunc = exec.LookPath
	runRsyncCommandFunc    rsyncCommandRunner = runRsyncCommand
)
```

Add `os/exec` to imports.

Add dependency checks:

```go
func ensureCopyBinary(name string) error {
	if _, err := lookPathCopyBinaryFunc(name); err != nil {
		return fmt.Errorf("%s CLI not found in PATH", name)
	}
	return nil
}
```

Add the default runner:

```go
func runRsyncCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Build rsync args from the shared SSH plan**

Add these helpers:

```go
func runVMRsyncCopy(ctx context.Context, req copyRequest, direction copyDirection, remote copyEndpoint, remoteCtx copyRemoteContext) error {
	if err := ensureCopyBinary("ssh"); err != nil {
		return err
	}
	if err := ensureCopyBinary("rsync"); err != nil {
		return err
	}
	plan, err := vmSSHExecutionPlanForServiceInfo(remoteCtx.Host, remoteCtx.Server, remote.Service, remoteCtx.Service, nil, nil, req.ForceProxy)
	if err != nil {
		return err
	}
	args, err := vmRsyncArgs(req, direction, remote, plan)
	if err != nil {
		return err
	}
	return runRsyncPlan(ctx, args, plan, remote.Service, os.Stdout, os.Stderr)
}

func vmRsyncArgs(req copyRequest, direction copyDirection, remote copyEndpoint, plan sshExecutionPlan) ([]string, error) {
	remoteShell, target, err := rsyncRemoteShellAndTarget(plan)
	if err != nil {
		return nil, err
	}
	args := []string{copyRsyncFlags(req), "-e", remoteShell}
	remoteSpec := target + ":" + remote.Path
	switch direction {
	case copyDirectionToRemote:
		args = append(args, req.Src.Path, remoteSpec)
	case copyDirectionFromRemote:
		args = append(args, remoteSpec, req.Dst.Path)
	default:
		return nil, fmt.Errorf("invalid copy direction")
	}
	return args, nil
}

func copyRsyncFlags(req copyRequest) string {
	var flags strings.Builder
	flags.WriteByte('-')
	if req.Archive {
		flags.WriteByte('a')
	} else if req.Recursive {
		flags.WriteByte('r')
	}
	if req.Verbose {
		flags.WriteByte('v')
	}
	if req.Compress {
		flags.WriteByte('z')
	}
	if flags.Len() == 1 {
		flags.WriteByte('a')
	}
	return flags.String()
}

func rsyncRemoteShellAndTarget(plan sshExecutionPlan) (string, string, error) {
	if len(plan.Args) == 0 {
		return "", "", fmt.Errorf("VM SSH plan is empty")
	}
	target := plan.Args[len(plan.Args)-1]
	sshArgs := append([]string{"ssh"}, plan.Args[:len(plan.Args)-1]...)
	return shellJoin(sshArgs), target, nil
}
```

- [ ] **Step 5: Implement rsync known-host retry and guest rsync hint**

Add:

```go
func runRsyncPlan(ctx context.Context, args []string, plan sshExecutionPlan, service string, stdout, stderr io.Writer) error {
	if strings.TrimSpace(plan.Notice) != "" {
		if _, err := fmt.Fprintln(writerOrDiscard(stderr), plan.Notice); err != nil {
			return err
		}
	}
	if !plan.canRepairKnownHost() {
		var stderrBuf bytes.Buffer
		err := runRsyncCommandFunc(ctx, args, stdout, &stderrBuf)
		replaySSHStderr(&stderrBuf, stderr)
		return withGuestRsyncHint(err, stderrBuf.String(), service)
	}

	var stderrBuf bytes.Buffer
	firstErr := runRsyncCommandFunc(ctx, args, stdout, &stderrBuf)
	if firstErr == nil {
		replaySSHStderr(&stderrBuf, stderr)
		return nil
	}
	if !shouldRepairSSHKnownHostError(stderrBuf.String(), *plan.KnownHostRepair) {
		replaySSHStderr(&stderrBuf, stderr)
		return withGuestRsyncHint(firstErr, stderrBuf.String(), service)
	}
	for _, alias := range plan.KnownHostRepair.Aliases() {
		if err := removeSSHKnownHostFunc(ctx, alias, plan.KnownHostRepair.KnownHostsFile); err != nil {
			return err
		}
	}
	return runRsyncCommandFunc(ctx, args, stdout, stderr)
}

func withGuestRsyncHint(err error, stderrText string, service string) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(stderrText)
	if strings.Contains(lower, "rsync: command not found") ||
		strings.Contains(lower, "rsync: not found") ||
		strings.Contains(lower, "bash: rsync: command not found") ||
		strings.Contains(lower, "sh: rsync: not found") {
		return fmt.Errorf("%w\nremote rsync is not available on VM %q; install rsync in the guest or use an official yeet VM image", err, service)
	}
	return err
}
```

Add `bytes` to imports if not already present.

- [ ] **Step 6: Add guest rsync hint and known-host retry tests**

Add tests for `withGuestRsyncHint` and retry:

```go
func TestWithGuestRsyncHint(t *testing.T) {
	err := withGuestRsyncHint(errors.New("exit status 127"), "bash: rsync: command not found\n", "devbox")
	if err == nil || !strings.Contains(err.Error(), "remote rsync is not available on VM \"devbox\"") {
		t.Fatalf("hint error = %v", err)
	}
}
```

```go
func TestRunRsyncPlanRepairsKnownHostOnce(t *testing.T) {
	oldRun := runRsyncCommandFunc
	oldRemove := removeSSHKnownHostFunc
	defer func() {
		runRsyncCommandFunc = oldRun
		removeSSHKnownHostFunc = oldRemove
	}()

	var runs int
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		runs++
		if runs == 1 {
			_, _ = io.WriteString(stderr, "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nOffending ED25519 key in /tmp/known_hosts:3\n")
			return errors.New("exit status 255")
		}
		return nil
	}
	var removed []string
	removeSSHKnownHostFunc = func(ctx context.Context, alias, knownHosts string) error {
		removed = append(removed, alias+"@"+knownHosts)
		return nil
	}

	plan := sshExecutionPlan{
		Args: []string{"-o", "HostName=192.168.100.12", "root@yeet-lab"},
		KnownHostRepair: &sshKnownHostRepair{
			Alias:          "yeet-vm-devbox@yeet-lab",
			KnownHostsFile: "/tmp/known_hosts",
		},
	}
	if err := runRsyncPlan(context.Background(), []string{"-avz"}, plan, "devbox", io.Discard, io.Discard); err != nil {
		t.Fatalf("runRsyncPlan: %v", err)
	}
	if runs != 2 || len(removed) != 1 {
		t.Fatalf("runs=%d removed=%v, want one repair and retry", runs, removed)
	}
}
```

- [ ] **Step 7: Run VM rsync tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunVMRsyncCopy|TestWithGuestRsyncHint|TestRunRsyncPlanRepairsKnownHostOnce' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit VM rsync copy**

```bash
git add pkg/yeet/copy_cmd.go pkg/yeet/copy_cmd_test.go
git commit -m "copy: rsync VM guest files"
```

## Task 6: CLI Help And Docs

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-llm.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/changelog.mdx` only during release task, not in this task

- [ ] **Step 1: Write failing CLI help test**

In `cmd/yeet/cli_test.go`, add:

```go
func TestCopyHelpMentionsVMGuestCopy(t *testing.T) {
	stdout, _, err := captureCLIOutput([]string{"yeet", "copy", "--help"})
	if err != nil {
		t.Fatalf("copy --help: %v", err)
	}
	for _, want := range []string{
		"Copy files between local paths and service data or VM guests",
		"[--force-proxy] [-avz] <src> <dst>",
		"yeet copy ./config.yml svc:data/config.yml",
		"yeet copy ./app devbox:~/app",
		"yeet copy --force-proxy ./configs/ devbox:~/configs/",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("copy help missing %q\n%s", want, stdout)
		}
	}
}
```

- [ ] **Step 2: Run CLI help test and verify failure**

Run:

```bash
mise exec -- go test ./cmd/yeet -run TestCopyHelpMentionsVMGuestCopy -count=1
```

Expected: FAIL because help still says only service data and lacks VM examples.

- [ ] **Step 3: Update copy command metadata**

In `pkg/cli/cli.go`, replace the `remoteCommandInfos["copy"]` entry with:

```go
"copy": {Name: "copy", Description: "Copy files between local paths and service data or VM guests", Usage: "[--force-proxy] [-avz] <src> <dst>", Examples: []string{
	"yeet copy ./config.yml svc:data/config.yml",
	"yeet copy ./configs/ svc:data/",
	"yeet copy svc:data/configs ./configs",
	"yeet copy ./app devbox:~/app",
	"yeet copy devbox:/var/log/cloud-init.log ./logs/",
	"yeet copy --force-proxy ./configs/ devbox:~/configs/",
}, Aliases: []string{"cp"}},
```

- [ ] **Step 4: Update website copy docs**

In `website/docs/cli/yeet-cli.mdx`, replace the first copy paragraph with:

```mdx
Copy files between local paths and a remote endpoint. For regular services,
`svc:path` targets the service `data/` directory. For VM services, `vm:path`
targets the VM guest filesystem and uses rsync over the same transport as
`yeet ssh`.
```

Add VM examples after the existing regular-service examples:

```mdx
# Local -> VM guest
yeet copy ./app devbox:~/app
yeet copy ./nginx.conf devbox:/etc/nginx/nginx.conf

# VM guest -> local
yeet copy devbox:/var/log/cloud-init.log ./logs/

# Force the catch proxy path for VM copy
yeet copy --force-proxy ./configs/ devbox:~/configs/
```

Add these notes in the copy Notes list:

```mdx
- VM copy requires `rsync` locally and in the guest. Official yeet VM images
  include it; custom images should install it.
- `--force-proxy` applies only to VM endpoints and mirrors
  `yeet ssh --force-proxy`.
- VM endpoints allow absolute guest paths. Regular service endpoints remain
  relative to service `data/`.
```

In `website/docs/payloads/vms.mdx`, add a short section near the SSH examples:

```mdx
## Copy Files

Use `yeet copy` with a VM endpoint to sync files into or out of the guest:

```bash
yeet copy ./app devbox:~/app
yeet copy devbox:/var/log/cloud-init.log ./logs/
yeet copy --force-proxy ./configs/ devbox:~/configs/
```

VM copy uses rsync over the same direct or proxied SSH path as `yeet ssh`.
```
```

- [ ] **Step 5: Regenerate agent help reference**

Run:

```bash
tools/generate-yeet-help-llm.sh
```

Expected: `.codex/skills/yeet-cli/references/yeet-help-llm.md` updates with the new copy help.

- [ ] **Step 6: Run docs/help checks**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestCopyHelpMentionsVMGuestCopy|TestHelp' -count=1
git -C website diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 7: Commit docs/help**

Commit website docs inside the submodule first:

```bash
git -C website add docs/cli/yeet-cli.mdx docs/payloads/vms.mdx
git -C website commit -m "docs: explain VM guest copy"
```

Then commit root changes:

```bash
git add pkg/cli/cli.go cmd/yeet/cli_test.go .codex/skills/yeet-cli/references/yeet-help-llm.md website
git commit -m "docs: document VM guest copy"
```

## Task 7: Official VM Images Include Rsync

**Files:**
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`
- Modify: `tools/vm-image/build_ubuntu_test.go`
- Modify: `tools/vm-image/README.md`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/nixos/yeet-vm.nix`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Update Ubuntu image builder tests**

In `tools/vm-image/build_ubuntu_test.go`, extend the existing string assertions so the test requires rsync installation and validation:

```go
for _, want := range []string{
	`apt-get install -y --no-install-recommends iptables nftables rsync`,
	`chroot "$root" /usr/bin/rsync --version >/dev/null`,
	`version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v14}"`,
} {
	if !strings.Contains(script, want) {
		t.Fatalf("build script missing %q", want)
	}
}
```

Run:

```bash
mise exec -- go test ./tools/vm-image -count=1
```

Expected: FAIL until the builder is updated.

- [ ] **Step 2: Update in-repo Ubuntu image builder**

In `tools/vm-image/build-ubuntu-26.04.sh`, change:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v13}"
```

to:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v14}"
```

Change the package install line to:

```bash
apt-get install -y --no-install-recommends iptables nftables rsync
```

Inside `validate_fast_rootfs_ubuntu_compatibility`, after the iptables backend check, add:

```bash
	chroot "$root" /usr/bin/rsync --version >/dev/null
```

Update `tools/vm-image/README.md` from `ubuntu-26.04-amd64-v13` to `ubuntu-26.04-amd64-v14` and mention that the rootfs includes rsync for VM guest copy.

- [ ] **Step 3: Mirror Ubuntu builder changes to yeet-vm-images**

Apply the same script changes to `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`.

In `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`, change:

```yaml
default: ubuntu-26.04-amd64-v13
```

to:

```yaml
default: ubuntu-26.04-amd64-v14
```

Update `/Users/shayne/code/yeet-vm-images/README.md` from `ubuntu-26.04-amd64-v13` to `ubuntu-26.04-amd64-v14` and mention rsync in the Ubuntu profile summary.

- [ ] **Step 4: Add rsync to NixOS image**

In `/Users/shayne/code/yeet-vm-images/nixos/yeet-vm.nix`, add `rsync` to `environment.systemPackages`:

```nix
      rsync
```

Place it near `openssh`, `procps`, and `sudo`.

In `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`, add:

```bash
assert_json "environment.systemPackages" 'map(tostring) | any(contains("rsync"))' "rsync must be installed for yeet VM copy"
```

In `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`, change:

```bash
version="${YEET_VM_IMAGE_VERSION:-nixos-26.05-amd64-v12}"
```

to:

```bash
version="${YEET_VM_IMAGE_VERSION:-nixos-26.05-amd64-v13}"
```

In `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`, change:

```yaml
default: nixos-26.05-amd64-v12
```

to:

```yaml
default: nixos-26.05-amd64-v13
```

Update `/Users/shayne/code/yeet-vm-images/README.md` from `nixos-26.05-amd64-v12` to `nixos-26.05-amd64-v13` and mention rsync in the NixOS profile summary.

- [ ] **Step 5: Run image repository checks**

Run:

```bash
mise exec -- go test ./tools/vm-image -count=1
bash -n tools/vm-image/build-ubuntu-26.04.sh
git -C /Users/shayne/code/yeet-vm-images diff --check
(
  cd /Users/shayne/code/yeet-vm-images
  mise exec -- bash -n scripts/build-ubuntu-26.04.sh scripts/build-nixos-26.05.sh scripts/verify-nixos-26.05.sh
  mise exec -- scripts/verify-nixos-26.05.sh
)
```

Expected: all commands pass.

- [ ] **Step 6: Commit image repo changes**

Commit external image repo changes:

```bash
git -C /Users/shayne/code/yeet-vm-images add scripts/build-ubuntu-26.04.sh .github/workflows/build-ubuntu-26.04.yml nixos/yeet-vm.nix scripts/verify-nixos-26.05.sh scripts/build-nixos-26.05.sh .github/workflows/build-nixos-26.05.yml README.md
git -C /Users/shayne/code/yeet-vm-images commit -m "images: include rsync for VM copy"
```

Commit yeet repo builder mirror:

```bash
git add tools/vm-image/build-ubuntu-26.04.sh tools/vm-image/build_ubuntu_test.go tools/vm-image/README.md
git commit -m "vm-image: include rsync in ubuntu rootfs"
```

## Task 8: Publish Images And Update Yeet Defaults

**Files:**
- Modify: `pkg/catch/vm_image_registry.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `pkg/catch/vm_images_cmd_test.go`
- Modify: `website/docs/changelog.mdx`

- [ ] **Step 1: Push image repository and publish image releases**

Run:

```bash
git -C /Users/shayne/code/yeet-vm-images push origin main
gh workflow run build-ubuntu-26.04.yml -R yeetrun/yeet-vm-images -f version=ubuntu-26.04-amd64-v14 -f yeet_ref=main -f publish_latest_alias=true
gh workflow run build-nixos-26.05.yml -R yeetrun/yeet-vm-images -f version=nixos-26.05-amd64-v13 -f yeet_ref=main -f publish_latest_alias=true
```

Watch both runs:

```bash
gh run list -R yeetrun/yeet-vm-images --workflow build-ubuntu-26.04.yml --limit 1
gh run list -R yeetrun/yeet-vm-images --workflow build-nixos-26.05.yml --limit 1
```

Expected: both workflows succeed and publish immutable plus latest-alias releases.

- [ ] **Step 2: Verify image release assets**

Run:

```bash
gh release view ubuntu-26.04-amd64-v14 -R yeetrun/yeet-vm-images --json tagName,assets --jq '{tagName, assets:[.assets[].name]}'
gh release view nixos-26.05-amd64-v13 -R yeetrun/yeet-vm-images --json tagName,assets --jq '{tagName, assets:[.assets[].name]}'
```

Expected: each release includes `manifest.json`, `rootfs.ext4.zst`, `vmlinux`, `firecracker`, and `checksums.txt`.

- [ ] **Step 3: Update yeet Ubuntu default version**

In `pkg/catch/vm_image_registry.go`, change:

```go
defaultVMImageVersion = "ubuntu-26.04-amd64-v13"
```

to:

```go
defaultVMImageVersion = "ubuntu-26.04-amd64-v14"
```

Update tests that assert the default Ubuntu version from v13 to v14:

- `pkg/catch/vm_image_test.go`
- `pkg/catch/vm_images_cmd_test.go`

Keep fixture versions such as v1, v3, v7, v8, and v12 when those tests are intentionally exercising stale cache or older image behavior.

- [ ] **Step 4: Add changelog entry**

In `website/docs/changelog.mdx`, add a new `v0.6.16` patch section with two bullets:

```mdx
### v0.6.16

- `yeet copy` now syncs VM endpoints into the guest filesystem with rsync over the same direct or proxied SSH path as `yeet ssh`.
- Official Ubuntu and NixOS VM images include rsync for VM guest file copy.
```

- [ ] **Step 5: Run image default tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestDefaultVMImageVersionUsesLatestFastBundle|TestVMImages|TestOfficialVMImage' -count=1
git -C website diff --check
```

Expected: PASS.

- [ ] **Step 6: Commit yeet default and changelog**

Commit website first:

```bash
git -C website add docs/changelog.mdx
git -C website commit -m "docs: add VM copy changelog"
```

Commit root:

```bash
git add pkg/catch/vm_image_registry.go pkg/catch/vm_image_test.go pkg/catch/vm_images_cmd_test.go website
git commit -m "vm-image: default ubuntu image to v14"
```

## Task 9: Local And Live Verification

**Files:**
- No new files unless a verification note is requested.

- [ ] **Step 1: Run targeted local tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./pkg/cli ./cmd/yeet ./pkg/catch ./tools/vm-image -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full local test suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Build local yeet binary for live testing**

Run:

```bash
mise exec -- go build -o /tmp/yeet-vm-copy ./cmd/yeet
```

Expected: `/tmp/yeet-vm-copy` exists.

- [ ] **Step 4: Update catch on lab-host**

Run:

```bash
/tmp/yeet-vm-copy --host=yeet-lab init root@lab-host
```

Expected: catch installs successfully and reports the local build.

- [ ] **Step 5: Live-test Ubuntu VM copy**

Run:

```bash
svc="copy-ubuntu-$(date +%s)"
tmp="$(mktemp -d)"
printf 'hello from yeet copy\n' >"$tmp/source.txt"
/tmp/yeet-vm-copy --host=yeet-lab run "$svc" vm://ubuntu/26.04 --net=svc,lan
/tmp/yeet-vm-copy --host=yeet-lab copy "$tmp/source.txt" "$svc:~/source.txt"
/tmp/yeet-vm-copy --host=yeet-lab ssh "$svc" -- sh -lc 'test "$(cat ~/source.txt)" = "hello from yeet copy" && command -v rsync'
/tmp/yeet-vm-copy --host=yeet-lab copy "$svc:~/source.txt" "$tmp/downloaded.txt"
cmp "$tmp/source.txt" "$tmp/downloaded.txt"
/tmp/yeet-vm-copy --host=yeet-lab rm "$svc" --clean-data --yes
rm -rf "$tmp"
```

Expected: upload, guest check, download, compare, and cleanup all succeed.

- [ ] **Step 6: Live-test NixOS VM copy**

Run:

```bash
svc="copy-nixos-$(date +%s)"
tmp="$(mktemp -d)"
printf 'hello from nixos copy\n' >"$tmp/source.txt"
/tmp/yeet-vm-copy --host=yeet-lab run "$svc" vm://nixos/26.05 --net=svc
/tmp/yeet-vm-copy --host=yeet-lab copy "$tmp/source.txt" "$svc:~/source.txt"
/tmp/yeet-vm-copy --host=yeet-lab ssh "$svc" -- sh -lc 'test "$(cat ~/source.txt)" = "hello from nixos copy" && command -v rsync'
/tmp/yeet-vm-copy --host=yeet-lab copy "$svc:~/source.txt" "$tmp/downloaded.txt"
cmp "$tmp/source.txt" "$tmp/downloaded.txt"
/tmp/yeet-vm-copy --host=yeet-lab rm "$svc" --clean-data --yes
rm -rf "$tmp"
```

Expected: upload, guest check, download, compare, and cleanup all succeed.

- [ ] **Step 7: Verify regular service copy still works**

Use an existing disposable non-VM service or create one with a tiny file payload. Then run:

```bash
svc="copy-service-$(date +%s)"
tmp="$(mktemp -d)"
cat >"$tmp/app.sh" <<'SH'
#!/bin/sh
sleep infinity
SH
chmod +x "$tmp/app.sh"
/tmp/yeet-vm-copy --host=yeet-lab run "$svc" "$tmp/app.sh"
printf 'regular service data\n' >"$tmp/config.txt"
/tmp/yeet-vm-copy --host=yeet-lab copy "$tmp/config.txt" "$svc:config.txt"
/tmp/yeet-vm-copy --host=yeet-lab copy "$svc:config.txt" "$tmp/downloaded-service.txt"
cmp "$tmp/config.txt" "$tmp/downloaded-service.txt"
/tmp/yeet-vm-copy --host=yeet-lab rm "$svc" --clean-data --yes
rm -rf "$tmp"
```

Expected: regular service copy remains service-data based and succeeds.

- [ ] **Step 8: Run pre-commit and release gate**

Run:

```bash
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: both pass.

## Task 10: Patch Release And Push

**Files:**
- Modify: `website/docs/changelog.mdx` if the expected release version changed before execution.

- [ ] **Step 1: Confirm v0.6.16 is still the next patch version**

Run:

```bash
git tag --list 'v*' --sort=-version:refname | sed -n '1,5p'
```

Expected: latest tag is `v0.6.15`, making `v0.6.16` the next patch version. If a newer tag exists, stop and update this plan's changelog and tag commands to the actual next patch version before continuing.

- [ ] **Step 2: Verify changelog release heading**

Verify the changelog uses the concrete release heading:

Run:

```bash
rg -n 'v0\\.6\\.16' website/docs/changelog.mdx
```

Expected: one match in the new changelog section heading.

- [ ] **Step 3: Commit final changelog correction if needed**

If Step 1 required a changelog version correction:

```bash
git -C website add docs/changelog.mdx
git -C website commit -m "docs: correct VM copy changelog version"
git add website
git commit -m "docs: update website changelog"
```

If Step 1 confirmed `v0.6.16`, skip this commit step.

- [ ] **Step 4: Push website**

Run:

```bash
git -C website push origin main
```

Expected: website `main` pushed.

- [ ] **Step 5: Create annotated tag**

```bash
git tag -a v0.6.16 -m "v0.6.16"
```

Expected: annotated tag created locally.

- [ ] **Step 6: Push root branch and tag**

Run:

```bash
git push origin main
git push origin v0.6.16
```

Expected: root `main` and tag pushed.

- [ ] **Step 7: Verify release workflow and assets**

Run:

```bash
gh run list --limit 5 --json databaseId,workflowName,headBranch,status,conclusion,displayTitle
```

Find the release workflow for the new tag and watch it:

```bash
gh run watch <run-id> --exit-status
gh release view v0.6.16 --json tagName,isDraft,isPrerelease,url,assets
```

Expected: release workflow succeeds and the public release contains yeet and catch tarballs plus checksums.

- [ ] **Step 8: Final status checks**

Run:

```bash
git status --short --branch
git -C website status --short --branch
git -C /Users/shayne/code/yeet-vm-images status --short --branch
```

Expected: all working trees clean and aligned with their remotes.

## Self-Review Checklist

- Spec coverage: VM endpoint guest copy, regular service preservation, `--force-proxy`, rsync dependency, direct/proxy transport, missing-rsync errors, image package availability, docs, tests, live lab-host verification, and patch release are each covered by tasks.
- Completeness scan: no deferred release versions or incomplete task wording should remain in this plan.
- Type consistency: `copyRequest.ForceProxy`, `copyRemoteContext`, `runVMRsyncCopyFunc`, `vmSSHExecutionPlanForServiceInfo`, `lookPathCopyBinaryFunc`, and `runRsyncCommandFunc` are introduced before later tasks depend on them.
