# Preferred Service Shells Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make interactive non-VM service sessions use a usable preferred host shell while preserving the service's existing identity, directory, environment contract, authorization, and explicit-command behavior.

**Architecture:** Keep the existing client and RPC routing unchanged. Add Catch-side shell candidate resolution beside the existing passwd helpers, apply it only when `ExecTargetServiceShell` receives no command, and keep identity selection independent from shell selection. Document that Compose sessions remain host-side root shells rather than container shells.

**Tech Stack:** Go standard library (`os`, `path/filepath`, `syscall`), Catch RPC PTY execution, Go `testing`, Mintlify MDX documentation, GitButler.

## Global Constraints

- Interactive selection order is native service account, Catch install user, root, then `/bin/sh`; Compose starts at the Catch install user.
- Reject configured shells named `nologin` or `false`, plus missing, directory, and non-executable paths.
- Native UID, GID, cleared supplementary groups, service-data `HOME`, working directory, and `ssh` permission remain unchanged.
- Compose keeps Catch's host identity, existing environment, and service-data working directory; it does not enter a container.
- Explicit commands, host shells, VM guest SSH, RPC types, and CLI syntax remain unchanged.
- Do not push the root or website repositories unless the user separately requests publication.

---

### Task 1: Select a Preferred Shell Without Changing Service Identity

**Files:**
- Modify: `pkg/catch/tty_exec.go:547-700`
- Test: `pkg/catch/tty_test.go:202-315`
- Test: `pkg/catch/tty_test.go:486-515`

**Interfaces:**
- Consumes: `passwdEntryForUser(string) (passwdEntry, bool)`, `effectiveServiceIdentity(db.ServiceView) resolvedServiceIdentity`, and `ttyExecer.newCmd(string, ...string) *exec.Cmd`.
- Produces: `passwdEntryForServiceIdentity(db.ServiceIdentity) (passwdEntry, bool)`, `(*ttyExecer).preferredServiceShellPath(db.ServiceIdentity) string`, `usableInteractiveShell(string) bool`, `replaceEnvironmentValue([]string, string, string) []string`, and `serviceShellEnvironment([]string, string, string, string) []string`.

- [ ] **Step 1: Add failing preferred-shell policy tests**

Add these test-only helpers to `pkg/catch/tty_test.go` so every selected shell except the final `/bin/sh` fallback is a real filesystem entry:

```go
func writeTestShell(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	return path
}

func setTestPasswd(t *testing.T, entries ...string) {
	t.Helper()
	oldPasswd := passwdFilePath
	t.Cleanup(func() { passwdFilePath = oldPasswd })
	path := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(path, []byte(strings.Join(append(entries, ""), "\n")), 0o600); err != nil {
		t.Fatalf("write passwd: %v", err)
	}
	passwdFilePath = path
}
```

Add table-driven coverage with literal expected paths:

```go
func TestPreferredServiceShellPath(t *testing.T) {
	nativeShell := writeTestShell(t, "native-shell", 0o700)
	installShell := writeTestShell(t, "install-shell", 0o700)
	rootShell := writeTestShell(t, "root-shell", 0o700)
	nonExecutable := writeTestShell(t, "non-executable", 0o600)
	shellDirectory := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing-shell")

	tests := []struct {
		name     string
		identity db.ServiceIdentity
		passwd   []string
		want     string
	}{
		{name: "named native", identity: db.ServiceIdentity{RequestedUser: "app", UID: 1002}, passwd: []string{"app:x:1002:1003::/srv/app:" + nativeShell, "deploy:x:1000:1000::/home/deploy:" + installShell, "root:x:0:0::/root:" + rootShell}, want: nativeShell},
		{name: "numeric native", identity: db.ServiceIdentity{RequestedUser: "1002", UID: 1002}, passwd: []string{"app:x:1002:1003::/srv/app:" + nativeShell, "deploy:x:1000:1000::/home/deploy:" + installShell, "root:x:0:0::/root:" + rootShell}, want: nativeShell},
		{name: "native nologin", identity: db.ServiceIdentity{RequestedUser: "app", UID: 1002}, passwd: []string{"app:x:1002:1003::/nonexistent:/usr/sbin/nologin", "deploy:x:1000:1000::/home/deploy:" + installShell, "root:x:0:0::/root:" + rootShell}, want: installShell},
		{name: "native false", identity: db.ServiceIdentity{RequestedUser: "app", UID: 1002}, passwd: []string{"app:x:1002:1003::/nonexistent:/bin/false", "deploy:x:1000:1000::/home/deploy:" + installShell, "root:x:0:0::/root:" + rootShell}, want: installShell},
		{name: "missing native", identity: db.ServiceIdentity{RequestedUser: "app", UID: 1002}, passwd: []string{"deploy:x:1000:1000::/home/deploy:" + installShell, "root:x:0:0::/root:" + rootShell}, want: installShell},
		{name: "missing install shell", passwd: []string{"deploy:x:1000:1000::/home/deploy:" + missing, "root:x:0:0::/root:" + rootShell}, want: rootShell},
		{name: "non-executable install shell", passwd: []string{"deploy:x:1000:1000::/home/deploy:" + nonExecutable, "root:x:0:0::/root:" + rootShell}, want: rootShell},
		{name: "directory install shell", passwd: []string{"deploy:x:1000:1000::/home/deploy:" + shellDirectory, "root:x:0:0::/root:" + rootShell}, want: rootShell},
		{name: "sh fallback", passwd: []string{"deploy:x:1000:1000::/nonexistent:/usr/sbin/nologin", "root:x:0:0::/nonexistent:/bin/false"}, want: "/bin/sh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestPasswd(t, tt.passwd...)
			server := newTestServer(t)
			server.cfg.InstallUser = "deploy"
			if got := (&ttyExecer{s: server}).preferredServiceShellPath(tt.identity); got != tt.want {
				t.Fatalf("preferredServiceShellPath() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

The production mutation these cases catch is accepting an unusable account shell or choosing a lower-priority shell before a valid higher-priority candidate.

- [ ] **Step 2: Add failing command-construction tests**

Replace the hardcoded `/bin/sh` expectation in `TestServiceShellCommandUsesPersistedNativeIdentity` with a real preferred shell path. Keep the existing literal assertions for UID, GID, empty supplementary groups, PTY attributes, directory, `HOME`, `USER`, and `LOGNAME`, and require `SHELL` to equal the selected path.

Add a Compose case that asserts:

```go
if cmd.Path != installShell || cmd.Args[0] != installShell {
	t.Fatalf("compose shell = path %q args %#v, want %q", cmd.Path, cmd.Args, installShell)
}
if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential != nil {
	t.Fatalf("compose shell credentials = %#v, want Catch identity", cmd.SysProcAttr)
}
if got := envValue(cmd.Env, "SHELL"); got != installShell {
	t.Fatalf("compose shell env = %q, want %q", got, installShell)
}
```

Add an explicit native command case with preferred shells present and assert that `serviceShellCommand([]string{"printf", "ok"})` still has path `printf`, argv `[]string{"printf", "ok"}`, and `SHELL=/bin/sh`.

The production mutations these tests catch are applying shell preference to explicit commands, losing service credentials, or failing to expose the selected interactive shell through `SHELL`.

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPreferredServiceShellPath|TestServiceShellCommand' -count=1
```

Expected: FAIL because preferred service-shell selection is not implemented and the current command path remains `/bin/sh`.

- [ ] **Step 4: Implement usable-shell resolution**

In `pkg/catch/tty_exec.go`, keep `defaultShellPath` unchanged for host sessions. Add service-only helpers with this behavior:

```go
func (e *ttyExecer) preferredServiceShellPath(identity db.ServiceIdentity) string {
	entries := make([]passwdEntry, 0, 3)
	if entry, ok := passwdEntryForServiceIdentity(identity); ok {
		entries = append(entries, entry)
	}
	if e != nil && e.s != nil {
		if entry, ok := passwdEntryForUser(strings.TrimSpace(e.s.cfg.InstallUser)); ok {
			entries = append(entries, entry)
		}
	}
	if entry, ok := passwdEntryForUser("root"); ok {
		entries = append(entries, entry)
	}
	for _, entry := range entries {
		if usableInteractiveShell(entry.shell) {
			return entry.shell
		}
	}
	return "/bin/sh"
}

func usableInteractiveShell(path string) bool {
	path = strings.TrimSpace(path)
	switch filepath.Base(path) {
	case "nologin", "false":
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}
```

Extend `passwdEntry` with the UID field from `/etc/passwd`. Add `passwdEntryForServiceIdentity` so a named identity first matches `RequestedUser`, while a numeric identity can match the persisted UID against the passwd UID field. Do not change host-shell fallback semantics.

- [ ] **Step 5: Apply selection only to interactive service sessions**

Refactor `serviceShellCommand` so it loads the service view and effective native identity before constructing the command. Use the preferred path only for `len(args) == 0`; use the supplied executable and argv unchanged otherwise.

Change the environment helper to accept the shell explicitly:

```go
func serviceShellEnvironment(base []string, home, userName, shell string) []string
```

For an interactive native shell, pass the selected path. For an explicit native command, pass `/bin/sh` to preserve existing behavior. For an interactive Compose shell, replace only `SHELL`:

```go
cmd.Env = replaceEnvironmentValue(cmd.Env, "SHELL", cmd.Path)
```

`replaceEnvironmentValue` must preserve order and unrelated values, remove every prior entry for the named key, and append exactly one `key=value` entry.

- [ ] **Step 6: Format and verify GREEN**

Run:

```bash
mise exec -- gofmt -w pkg/catch/tty_exec.go pkg/catch/tty_test.go
mise exec -- go test ./pkg/catch -run 'TestPreferredServiceShellPath|TestServiceShellCommand' -count=1
mise exec -- go test ./pkg/catch -count=1
```

Expected: all focused and package tests PASS with no warnings.

- [ ] **Step 7: Checkpoint the Catch behavior**

Run `but diff`, select only `pkg/catch/tty_exec.go` and `pkg/catch/tty_test.go`, and create a checkpoint on `codex/preferred-service-shell` with message:

```text
ssh: use preferred shells for service sessions
```

---

### Task 2: Document Service Shell Selection and Compose Root Context

**Files:**
- Modify: `website/docs/cli/yeet-cli.mdx:584-588`

**Interfaces:**
- Consumes: the shell-selection and identity behavior completed in Task 1.
- Produces: evergreen manual wording that distinguishes native, Compose, host, and VM shell contexts.

- [ ] **Step 1: Update the manual paragraph**

Replace the existing regular-service explanation with concise evergreen wording equivalent to:

```mdx
After `yeet init`, host and regular service shells use catch over Tailscale and
do not require host SSH keys or a host password. Interactive regular service
shells use the service account's configured shell when it is usable, then the
catch host user's preferred shell, with `/bin/sh` as the fallback.

`yeet ssh <svc>` starts in the service data directory. Native services keep the
service's persisted UID and GID. Docker Compose services open a host-side shell
as the catch process identity, normally root; they do not enter a container.
VM services still connect to the guest operating system with SSH because the
guest has its own authentication boundary.
```

Do not mention private service names, hosts, implementation function names, or release timing.

- [ ] **Step 2: Validate the website edit**

Run:

```bash
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```

Expected: no whitespace errors and no new private infrastructure references.

- [ ] **Step 3: Preserve publication boundaries**

Because publication was not requested, leave the website edit uncommitted inside the submodule and do not move the root gitlink. Report that state explicitly. When publication is later authorized, commit and push the website repository first, then checkpoint the root gitlink according to `AGENTS.md`.

---

### Task 3: Verify the Integrated Local Change

**Files:**
- Verify: `pkg/catch/tty_exec.go`
- Verify: `pkg/catch/tty_test.go`
- Verify: `website/docs/cli/yeet-cli.mdx`
- Include in root checkpoint: `docs/superpowers/plans/2026-08-08-preferred-service-shells.md`

**Interfaces:**
- Consumes: the Catch implementation and manual wording from Tasks 1 and 2.
- Produces: a locally verified implementation checkpoint with publication and live-deployment state reported separately.

- [ ] **Step 1: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the required repository gate**

Run once after all root and website working-tree edits are stable:

```bash
mise exec -- pre-commit run --all-files
```

Expected: every hook PASS.

- [ ] **Step 3: Review exact local scope**

Run:

```bash
but diff
git -C website diff --check
git -C website status --short --branch
```

Confirm that root changes belong only to the preferred-shell session and that the website has only the intended manual edit.

- [ ] **Step 4: Checkpoint remaining root files**

Use the IDs from `but diff` to add the implementation plan to the existing `codex/preferred-service-shell` branch. If code remains uncommitted, include only the intended Catch files. Use a succinct message describing the remaining coherent unit.

- [ ] **Step 5: Report completion boundaries**

Report separately:

- local Catch code and tests committed on `codex/preferred-service-shell`;
- website manual edit present locally but not committed or pushed;
- nothing pushed to `origin/main`;
- Catch on `yeet-pve1` still runs the previously deployed version because live deployment was not authorized.
