# Init VM Tools and Tailscale Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make first-time `yeet init` install decisions consistent for Docker and VM host packages, and make Tailscale OAuth setup docs match the current Tailscale Trust credentials UI.

**Architecture:** Move the VM host package decision up into the local `yeet init` client flow, matching Docker: an explicit flag is unattended yes, an interactive TTY can ask, and non-interactive runs never try to prompt remotely. Keep catch-side VM setup as the installer/enforcer for `CATCH_INSTALL_VM_TOOLS=1`, but stop catch from issuing its own VM package prompt so users do not see EOF-driven prompt failures.

**Tech Stack:** Go CLI and catch install code, yargs metadata, Go unit tests, website MDX docs in the `website/` submodule, GitButler for repository publication.

---

## Files

- Modify: `pkg/yeet/init.go`
  - Add client-side VM host preflight and prompt logic near `prepareInitDockerInstall`.
  - Pass the resulting boolean to `installInitCatchWithTailscaleRetry`.
- Modify: `pkg/yeet/init_test.go`
  - Add tests for VM tool preflight and prompt behavior.
  - Add an injectable confirmation function if needed for deterministic prompt tests.
- Modify: `cmd/catch/vm_prereqs.go`
  - Update `vmHostRequirementsDocsURL` from `#vm-host-requirements` to `#host-requirements`.
  - Remove catch-side VM package prompting when `CATCH_INSTALL_VM_TOOLS` is not set.
- Modify: `cmd/catch/vm_prereqs_test.go`
  - Update tests for no remote VM prompt by default.
  - Keep tests for explicit `CATCH_INSTALL_VM_TOOLS=1` package installation.
- Modify: `pkg/yeet/init_install_filter_test.go`
  - Update expected docs anchors from `#vm-host-requirements` to `#host-requirements`.
- Modify: `cmd/yeet/cli.go`
  - Keep flags in usage, but make examples show `yeet init root@<machine-host>` as the normal path and reserve install flags for unattended setup.
- Modify: `.codex/skills/yeet-cli/references/yeet-help-llm.md`
  - Regenerate or patch help reference after CLI help changes.
- Modify: `README.md`
  - Update bootstrap docs to describe interactive prompts and unattended flags.
- Modify inside submodule: `website/docs/getting-started/installation.mdx`
  - Fix the anchor.
  - Simplify first-run setup.
  - Explain automatic/prompted VM tools behavior.
- Modify inside submodule: `website/docs/concepts/tailscale.mdx`
  - Replace ambiguous OAuth setup text with Trust credentials UI language.
  - Explain broad and least-privilege OAuth options.
- Modify inside submodule: `website/docs/getting-started/first-run-validation.mdx`
  - Fix links to `#host-requirements` and remove `--install-vm-tools` as the default troubleshooting answer.

## Tailscale Documentation Facts To Use

- Tailscale OAuth clients are created from the admin console Trust credentials page: open Trust credentials, select Credential, select OAuth, select scopes, generate the credential, then copy the client ID and secret. Source: https://tailscale.com/docs/features/oauth-clients
- Tailscale documents `all` as complete access to all endpoints, including future endpoints. This is acceptable to mention as the simple path only when the user is comfortable with broad access. Source: https://tailscale.com/docs/reference/trust-credentials
- Tailscale documents `auth_keys` as the scope that can create/delete auth keys through `POST /api/v2/tailnet/:tailnet/keys`. Source: https://tailscale.com/docs/reference/trust-credentials
- OAuth token requests can include requested scopes and tags, and the OAuth client must have permission to grant both. Tailscale notes that an OAuth client with `all` can request all scopes and all device tags. Source: https://tailscale.com/docs/features/oauth-clients
- For least-privilege yeet setup, docs should say:
  - OAuth type: Trust credentials -> OAuth.
  - Scope: `auth_keys` write.
  - Tags: either select `tag:catch` directly for catch-only setup, or select an owner tag such as `tag:yeet` that owns `tag:catch` and future service tags such as `tag:app`.

---

### Task 1: Add client-side VM host preflight tests

**Files:**
- Modify: `pkg/yeet/init_test.go`
- Later implementation target: `pkg/yeet/init.go`

- [ ] **Step 1: Add test doubles for VM preflight**

Add temporary test-only expectations in `pkg/yeet/init_test.go` after the Docker prep tests. The implementation will add these package variables:

```go
remoteVMHostStatusFn = remoteVMHostStatus
confirmInitFn = cmdutil.Confirm
```

Use a fake status struct shaped like this in the test expectations:

```go
type initVMHostStatus struct {
	VMCapable       bool
	AptAvailable    bool
	MissingCommands []vmHostCommandRequirement
	MissingPackages []string
	Warnings        []string
}
```

- [ ] **Step 2: Write failing test for interactive VM tool confirmation**

Add this test:

```go
func TestPrepareInitVMToolsInstallPromptsForCapableAptHost(t *testing.T) {
	oldIsTerminal := isTerminalFn
	oldStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	defer func() {
		isTerminalFn = oldIsTerminal
		remoteVMHostStatusFn = oldStatus
		confirmInitFn = oldConfirm
	}()
	isTerminalFn = func(int) bool { return true }
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{
			VMCapable:       true,
			AptAvailable:    true,
			MissingCommands: []vmHostCommandRequirement{{Command: "qemu-img", Package: "qemu-utils"}},
			MissingPackages: []string{"qemu-utils"},
		}, nil
	}
	confirmedPrompt := ""
	confirmInitFn = func(_ io.Reader, _ io.Writer, msg string) (bool, error) {
		confirmedPrompt = msg
		return true, nil
	}

	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)
	got, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall returned error: %v", err)
	}
	if !got {
		t.Fatal("prepareInitVMToolsInstall = false, want true after confirmation")
	}
	if confirmedPrompt != "VM payloads can run on this host, but VM host packages are missing. Install them now?" {
		t.Fatalf("prompt = %q", confirmedPrompt)
	}
}
```

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestPrepareInitVMToolsInstallPromptsForCapableAptHost -count=1
```

Expected: fail because `remoteVMHostStatusFn`, `confirmInitFn`, `initVMHostStatus`, `vmHostCommandRequirement`, or `prepareInitVMToolsInstall` does not exist yet.

- [ ] **Step 3: Write failing test for explicit unattended flag**

Add:

```go
func TestPrepareInitVMToolsInstallHonorsExplicitFlag(t *testing.T) {
	oldStatus := remoteVMHostStatusFn
	defer func() { remoteVMHostStatusFn = oldStatus }()
	called := false
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		called = true
		return initVMHostStatus{}, nil
	}

	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)
	got, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{installVMTools: true})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall returned error: %v", err)
	}
	if !got {
		t.Fatal("prepareInitVMToolsInstall = false, want true")
	}
	if called {
		t.Fatal("explicit --install-vm-tools should not need remote preflight")
	}
}
```

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestPrepareInitVMToolsInstallHonorsExplicitFlag -count=1
```

Expected: fail until Task 2.

- [ ] **Step 4: Write failing test for non-VM-capable warning/no prompt**

Add:

```go
func TestPrepareInitVMToolsInstallSkipsNonVMCapableHost(t *testing.T) {
	oldIsTerminal := isTerminalFn
	oldStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	defer func() {
		isTerminalFn = oldIsTerminal
		remoteVMHostStatusFn = oldStatus
		confirmInitFn = oldConfirm
	}()
	isTerminalFn = func(int) bool { return true }
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{
			VMCapable: false,
			Warnings:  []string{"VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See https://yeetrun.com/docs/getting-started/installation#host-requirements"},
		}, nil
	}
	confirmInitFn = func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatal("should not prompt when the host is not VM-capable")
		return false, nil
	}

	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)
	got, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall returned error: %v", err)
	}
	if got {
		t.Fatal("prepareInitVMToolsInstall = true, want false")
	}
}
```

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestPrepareInitVMToolsInstallSkipsNonVMCapableHost -count=1
```

Expected: fail until Task 2.

---

### Task 2: Implement client-side VM host preflight

**Files:**
- Modify: `pkg/yeet/init.go`
- Test: `pkg/yeet/init_test.go`

- [ ] **Step 1: Add shared VM requirement types in `pkg/yeet/init.go`**

Add near the Docker init helpers:

```go
type vmHostCommandRequirement struct {
	Command string
	Package string
}

type initVMHostStatus struct {
	VMCapable       bool
	AptAvailable    bool
	MissingCommands []vmHostCommandRequirement
	MissingPackages []string
	Warnings        []string
}

var (
	remoteVMHostStatusFn = remoteVMHostStatus
	confirmInitFn        = cmdutil.Confirm
)
```

Use these constants:

```go
const hostRequirementsDocsURL = "https://yeetrun.com/docs/getting-started/installation#host-requirements"

var initVMHostCommandRequirements = []vmHostCommandRequirement{
	{Command: "qemu-img", Package: "qemu-utils"},
	{Command: "zstd", Package: "zstd"},
	{Command: "e2fsck", Package: "e2fsprogs"},
	{Command: "resize2fs", Package: "e2fsprogs"},
	{Command: "mount", Package: "util-linux"},
	{Command: "umount", Package: "util-linux"},
	{Command: "ip", Package: "iproute2"},
}
```

- [ ] **Step 2: Add remote VM preflight probe**

Add:

```go
func remoteVMHostStatus(userAtRemote string, goarch string) (initVMHostStatus, error) {
	script := `
set -eu
check_cmd() { if command -v "$1" >/dev/null 2>&1; then printf yes; else printf no; fi; }
check_path() { if [ -e "$1" ]; then printf yes; else printf no; fi; }
printf 'apt_get=%s\n' "$(check_cmd apt-get)"
printf 'kvm=%s\n' "$(check_path /dev/kvm)"
printf 'tun=%s\n' "$(check_path /dev/net/tun)"
for cmd in qemu-img zstd e2fsck resize2fs mount umount ip; do
  printf 'cmd_%s=%s\n' "$cmd" "$(check_cmd "$cmd")"
done
`
	cmd := exec.Command("ssh", userAtRemote, "bash -lc "+shellQuote(script))
	output, err := cmd.Output()
	if err != nil {
		return initVMHostStatus{}, fmt.Errorf("failed to inspect VM host prerequisites: %w", err)
	}
	return parseInitVMHostStatus(string(output), goarch), nil
}
```

Add `parseInitVMHostStatus` and helpers:

```go
func parseInitVMHostStatus(output string, goarch string) initVMHostStatus {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = value
		}
	}
	status := initVMHostStatus{AptAvailable: values["apt_get"] == "yes"}
	vmCapable := goarch == "amd64" && values["kvm"] == "yes" && values["tun"] == "yes"
	status.VMCapable = vmCapable
	if goarch != "amd64" {
		status.Warnings = append(status.Warnings, fmt.Sprintf("VM support is unavailable on this host: yeet VM payloads require x86_64/amd64 hosts in this release; detected %s. See %s", goarch, hostRequirementsDocsURL))
	}
	if values["kvm"] != "yes" {
		status.Warnings = append(status.Warnings, "VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See "+hostRequirementsDocsURL)
	}
	if values["tun"] != "yes" {
		status.Warnings = append(status.Warnings, "VM networking is unavailable on this host: /dev/net/tun is missing. See "+hostRequirementsDocsURL)
	}
	for _, req := range initVMHostCommandRequirements {
		if values["cmd_"+req.Command] != "yes" {
			status.MissingCommands = append(status.MissingCommands, req)
			if req.Package != "" {
				status.MissingPackages = append(status.MissingPackages, req.Package)
			}
		}
	}
	status.MissingPackages = sortedUniqueStrings(status.MissingPackages)
	return status
}
```

- [ ] **Step 3: Implement VM install decision**

Add:

```go
func prepareInitVMToolsInstall(ui *initUI, userAtRemote string, goarch string, opts initOptions) (bool, error) {
	ui.StartStep("Check VM host")
	if opts.installVMTools {
		ui.DoneStep("will install")
		return true, nil
	}
	status, err := remoteVMHostStatusFn(userAtRemote, goarch)
	if err != nil {
		ui.FailStep(err.Error())
		return false, err
	}
	for _, warning := range status.Warnings {
		ui.Warn("Warning: " + warning)
	}
	if len(status.MissingCommands) == 0 {
		ui.DoneStep("ready")
		return false, nil
	}
	if !status.VMCapable {
		ui.DoneStep("not available")
		return false, nil
	}
	if !status.AptAvailable {
		ui.Warn(fmt.Sprintf("Warning: VM tools are incomplete: missing %s. Install packages: %s. See %s", initVMHostCommandNames(status.MissingCommands), strings.Join(status.MissingPackages, ", "), hostRequirementsDocsURL))
		ui.DoneStep("manual packages needed")
		return false, nil
	}
	if !isTerminalFn(int(os.Stdin.Fd())) || !isTerminalFn(int(os.Stdout.Fd())) {
		ui.Warn(fmt.Sprintf("Warning: VM host packages are missing: %s. For unattended setup, rerun with --install-vm-tools. See %s", strings.Join(status.MissingPackages, ", "), hostRequirementsDocsURL))
		ui.DoneStep("packages skipped")
		return false, nil
	}
	ui.Suspend()
	ok, confirmErr := confirmInitFn(os.Stdin, os.Stdout, "VM payloads can run on this host, but VM host packages are missing. Install them now?")
	ui.Resume()
	if confirmErr != nil {
		ui.Warn(fmt.Sprintf("Warning: could not confirm VM package install (%v). For unattended setup, rerun with --install-vm-tools. See %s", confirmErr, hostRequirementsDocsURL))
		ui.DoneStep("packages skipped")
		return false, nil
	}
	if !ok {
		ui.DoneStep("packages skipped")
		return false, nil
	}
	ui.DoneStep("will install")
	return true, nil
}
```

Add:

```go
func initVMHostCommandNames(reqs []vmHostCommandRequirement) string {
	names := make([]string, 0, len(reqs))
	for _, req := range reqs {
		names = append(names, req.Command)
	}
	return strings.Join(names, ", ")
}
```

- [ ] **Step 4: Call VM decision from `initCatch`**

Change:

```go
return installInitCatchWithTailscaleRetry(ui, userAtRemote, useSudo, installDocker, opts.installVMTools, opts)
```

to:

```go
installVMTools, err := prepareInitVMToolsInstallFn(ui, userAtRemote, goarch, opts)
if err != nil {
	return err
}
return installInitCatchWithTailscaleRetry(ui, userAtRemote, useSudo, installDocker, installVMTools, opts)
```

Add `prepareInitVMToolsInstallFn = prepareInitVMToolsInstall` to the function variables block so tests can stub it.

- [ ] **Step 5: Run targeted tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestPrepareInitVMToolsInstall|TestInitCatch|TestRemoteCatchInstallArgs' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit checkpoint**

Use GitButler after confirming no unrelated changes are included:

```bash
but status -fv
but commit <branch> -m "init: move VM tool prompts client-side" --changes <ids>
```

---

### Task 3: Remove catch-side VM package prompt and fix warning anchor

**Files:**
- Modify: `cmd/catch/vm_prereqs.go`
- Modify: `cmd/catch/vm_prereqs_test.go`
- Modify: `pkg/yeet/init_install_filter_test.go`

- [ ] **Step 1: Write/update failing catch test for no prompt without env**

In `cmd/catch/vm_prereqs_test.go`, replace the test that expects prompt confirmation with:

```go
func TestSetupVMHostWarnsWithoutPromptWhenToolsMissing(t *testing.T) {
	prompted := false
	var stderr strings.Builder
	deps := vmSetupDeps{
		commandExists: func(name string) bool {
			return name == "apt-get"
		},
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			prompted = true
			return true, nil
		},
		stdin:  strings.NewReader("y\n"),
		stderr: &stderr,
		runCommand: func(string, ...string) error {
			t.Fatal("VM packages should install only when CATCH_INSTALL_VM_TOOLS=1")
			return nil
		},
		getenv: func(string) string { return "" },
		goarch: "amd64",
	}

	if err := setupVMHostWith(deps); err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if prompted {
		t.Fatal("setupVMHostWith prompted without CATCH_INSTALL_VM_TOOLS=1")
	}
	if got := stderr.String(); !strings.Contains(got, "Warning: VM tools are incomplete") {
		t.Fatalf("stderr = %q, want VM tools warning", got)
	}
}
```

Run:

```bash
mise exec -- go test ./cmd/catch -run TestSetupVMHostWarnsWithoutPromptWhenToolsMissing -count=1
```

Expected: fail until the prompt block is removed.

- [ ] **Step 2: Remove the catch-side prompt block**

In `cmd/catch/vm_prereqs.go`, change:

```go
warnMissingVMHostCommands(deps.stderr, report.MissingCommands, packages)
ok, err := deps.confirm(deps.stdin, deps.stderr, "Would you like to install VM host packages with apt-get?")
...
```

to:

```go
warnMissingVMHostCommands(deps.stderr, report.MissingCommands, packages)
return nil
```

Keep the `CATCH_INSTALL_VM_TOOLS=1` path unchanged.

- [ ] **Step 3: Fix docs URL constant**

Change:

```go
const vmHostRequirementsDocsURL = "https://yeetrun.com/docs/getting-started/installation#vm-host-requirements"
```

to:

```go
const vmHostRequirementsDocsURL = "https://yeetrun.com/docs/getting-started/installation#host-requirements"
```

- [ ] **Step 4: Update tests expecting old anchor**

In `pkg/yeet/init_install_filter_test.go` and `cmd/catch/vm_prereqs_test.go`, replace `#vm-host-requirements` with `#host-requirements`.

- [ ] **Step 5: Run targeted tests**

Run:

```bash
mise exec -- go test ./cmd/catch -run 'TestSetupVMHost|TestVMHost' -count=1
mise exec -- go test ./pkg/yeet -run TestInitInstallFilterSummarizesVisibleWarningsOnce -count=1
```

Expected: pass.

- [ ] **Step 6: Commit checkpoint**

```bash
but status -fv
but commit <branch> -m "catch: stop prompting for VM packages remotely" --changes <ids>
```

---

### Task 4: Refresh CLI help and docs

**Files:**
- Modify: `cmd/yeet/cli.go`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-llm.md`
- Modify: `README.md`
- Modify inside submodule: `website/docs/getting-started/installation.mdx`
- Modify inside submodule: `website/docs/concepts/tailscale.mdx`
- Modify inside submodule: `website/docs/getting-started/first-run-validation.mdx`

- [ ] **Step 1: Update CLI help examples**

In `cmd/yeet/cli.go`, keep usage flags but make examples:

```go
Examples: []string{
	"yeet init root@<machine-host>",
	"yeet init --ts-client-secret=<secret> root@<machine-host>",
	"yeet init --install-docker --install-vm-tools --ts-client-secret=<secret> root@<machine-host>",
	"yeet init --ts-auth-key=<key> root@<machine-host>",
	"yeet init",
},
```

- [ ] **Step 2: Update README bootstrap text**

Replace the host package section with this meaning:

```markdown
Run `yeet init root@<machine-host>` first. If Docker is missing and the command
is interactive, yeet asks whether to install Docker. If the host exposes KVM and
TUN/TAP and VM tools are missing, yeet asks whether to install the Debian/Ubuntu
VM packages too.

For unattended setup, pass the install flags up front:

```bash
yeet init --install-docker --install-vm-tools --ts-client-secret=<secret> root@<machine-host>
```

Use `--install-vm-tools` only as an unattended yes for hosts that expose KVM and
TUN/TAP. If the host cannot run VMs, yeet warns and container workloads still
work.
```

- [ ] **Step 3: Update installation docs**

In `website/docs/getting-started/installation.mdx`:

Replace the "Prepare Tailscale access" checklist with:

```markdown
Before you run `yeet init`, create a Tailscale OAuth credential:

1. Open the Tailscale admin console Trust credentials page:
   https://login.tailscale.com/admin/settings/trust-credentials
2. Select **Credential**, then **OAuth**.
3. For the fastest setup, choose **All - Read & Write** only if you are
   comfortable giving yeet broad tailnet API access.
4. For least privilege, choose custom scopes with **Auth Keys** write access
   (`auth_keys`) and select the tag the credential may assign.
5. Copy the `tskey-client-...` client secret. `yeet init` asks for this secret.
```

Then add the least-privilege tag guidance:

```markdown
For catch-only setup, the OAuth credential can assign `tag:catch`. If you plan
to use `--net=ts` services later, create an owner tag such as `tag:yeet`, let it
own `tag:catch` and service tags such as `tag:app`, then select `tag:yeet` on
the OAuth credential.
```

Update the host package section to describe interactive prompts and unattended flags as in the README.

- [ ] **Step 4: Update Tailscale concept docs**

In `website/docs/concepts/tailscale.mdx`, make the first-time setup section say:

```markdown
Use a Tailscale OAuth credential, not a pre-authentication auth key, for the
normal yeet setup. In the admin console, go to Trust credentials, create an
OAuth credential, then either:

- choose **All - Read & Write** if you are comfortable with broad API access; or
- choose custom scopes, grant **Auth Keys** write access (`auth_keys`), and
  select the tag the credential may assign.
```

Keep the existing policy snippet, but explain that the owner-tag pattern is for least privilege with future service tags.

- [ ] **Step 5: Fix docs anchors**

Replace all `#vm-host-requirements` links in repo docs and website docs with:

```text
#host-requirements
```

Run:

```bash
rg -n "vm-host-requirements" README.md website/docs pkg cmd .codex/skills
```

Expected: no output.

- [ ] **Step 6: Refresh help reference**

Run the repo's existing help generation/update path if available. If there is no generator, patch `.codex/skills/yeet-cli/references/yeet-help-llm.md` to match `yeet init --help` output.

Verify:

```bash
mise exec -- go run ./cmd/yeet init --help
rg -n "install-vm-tools|Trust credentials|host-requirements" README.md website/docs .codex/skills/yeet-cli/references/yeet-help-llm.md
```

Expected: help examples match CLI, docs contain Trust credentials guidance, and anchors use `host-requirements`.

- [ ] **Step 7: Commit website first, then root pointer**

Inside `website/`:

```bash
git -C website diff --check
git -C website status --short
git -C website add docs/getting-started/installation.mdx docs/concepts/tailscale.mdx docs/getting-started/first-run-validation.mdx
git -C website commit -m "docs: clarify init and tailscale oauth setup"
git -C website push origin main
```

Then commit root changes with GitButler:

```bash
but status -fv
but commit <branch> -m "docs: clarify init package prompts" --changes <ids>
```

Use the raw website git exception from `AGENTS.md` only for the submodule repository.

---

### Task 5: Full verification, live smoke, and finish

**Files:**
- No new files beyond prior tasks.

- [ ] **Step 1: Run targeted Go tests**

```bash
mise exec -- go test ./pkg/yeet ./cmd/catch -count=1
```

Expected: pass.

- [ ] **Step 2: Run full Go tests**

```bash
mise exec -- go test ./... -count=1
```

Expected: pass.

- [ ] **Step 3: Run docs checks**

```bash
git -C website diff --check
rg -n "vm-host-requirements" README.md website/docs pkg cmd .codex/skills
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```

Expected:
- no stale anchor output;
- no private path or private host leak output.

- [ ] **Step 4: Run pre-commit**

```bash
pre-commit run --all-files
```

Expected: pass. If it fails because hooks modify files, inspect the diff and commit only intended changes.

- [ ] **Step 5: Live smoke on `root@pve1` with a disposable command path**

Only after local tests pass, install the changed catch on `yeet-pve1`:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
```

Expected:
- Docker check remains normal.
- VM host check reports `ready` or skips prompting because pve1 already has VM tools.
- No remote `Would you like to install VM host packages with apt-get?` prompt appears.

If needed, temporarily simulate a missing VM command only in a controlled disposable test container or unit test. Do not remove real packages from pve1.

- [ ] **Step 6: Finish to main and push**

Use the repo GitButler flow:

```bash
but status -fv
but pull --check
```

Then squash/tidy the session branch if needed, publish to local `main` and `origin/main` using the documented no-PR finish flow in `AGENTS.md`, and verify:

```bash
git ls-remote origin refs/heads/main
```

Expected: `origin/main` contains the final root commit, and the website commit is already pushed.

---

## Self-Review

- Spec coverage: Docker consistency, VM prompts, non-VM warning behavior, stale anchor, and Tailscale OAuth docs are all covered.
- Placeholder scan: no `TBD`, no "add tests later", no unspecified files.
- Type consistency: `initVMHostStatus`, `vmHostCommandRequirement`, `remoteVMHostStatusFn`, and `confirmInitFn` are introduced before use.
- Scope check: this is one cohesive init/docs improvement; no release or image rebuild is included.
