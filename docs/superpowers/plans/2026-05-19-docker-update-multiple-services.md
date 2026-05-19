# Docker Update Multiple Services Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet docker update <service...>` update every requested compose service, including mixed `service@host` targets, while preserving `docker update --outdated`.

**Architecture:** Model `docker update` as a variadic service command in CLI metadata, keep the generic single-service bridge from consuming variadic service lists, and resolve explicit update targets inside `pkg/yeet`. Catch remains unchanged: each actual compose update is still sent as a scoped remote `docker update` with no positional service args.

**Tech Stack:** Go, yargs command metadata, yeet client routing, catch RPC exec, Go `testing`, markdown docs.

---

## File Structure

- Modify `pkg/cli/cli.go`: add `DockerUpdateArgs`, update `docker update` metadata, and teach service-argument detection to recognize `[]ServiceName`.
- Modify `pkg/cli/cli_test.go`: assert variadic metadata and slice service detection.
- Modify `cmd/yeet/cli_bridge.go`: skip automatic service bridging for variadic service commands.
- Modify `cmd/yeet/cli_bridge_test.go`: cover variadic docker update bridge behavior.
- Modify `cmd/yeet/cli_test.go`: cover `prepareCommandRoute` for `docker@host update foo bar`.
- Create `pkg/yeet/docker_update.go`: own explicit `docker update` target parsing, host resolution, duplicate removal, marker printing, and sequential execution.
- Modify `pkg/yeet/docker_outdated.go`: leave outdated scanning there and move explicit update handling into the new file.
- Create `pkg/yeet/docker_update_test.go`: test explicit target resolution and execution.
- Modify `pkg/yeet/docker_outdated_test.go`: keep existing outdated tests and add the override-mixing regression test.
- Modify `README.md`, `website/docs/cli/yeet-cli.mdx`, and `website/docs/operations/workflows.mdx`: document `<svc...>` and mixed-host examples.

## Task 1: CLI Metadata

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`

- [ ] **Step 1: Write the failing metadata tests**

Add these assertions inside `TestCommandRegistriesExposeMetadata` after the existing docker outdated metadata check:

```go
	updateArg, ok := yargs.ArgSpecAt(reg.Groups["docker"].Commands["update"].ArgsSchema, 0)
	if !ok {
		t.Fatal("docker update should expose variadic service arg metadata")
	}
	if !IsServiceArgSpec(updateArg) || !updateArg.Required || !updateArg.Variadic || updateArg.MinCount != 1 {
		t.Fatalf("docker update arg = %#v, want required variadic []ServiceName", updateArg)
	}
```

Extend `TestServiceArgSpecDetection`:

```go
	if !IsServiceArgSpec(yargs.ArgSpec{GoType: reflect.TypeOf([]ServiceName{})}) {
		t.Fatal("[]ServiceName arg spec was not detected")
	}
```

- [ ] **Step 2: Run the targeted failing test**

Run:

```bash
go test ./pkg/cli -run 'TestCommandRegistriesExposeMetadata|TestServiceArgSpecDetection' -count=1
```

Expected: FAIL because `docker update` still uses `ServiceArgs{}` and `IsServiceArgSpec` only recognizes scalar `ServiceName`.

- [ ] **Step 3: Implement the CLI metadata**

In `pkg/cli/cli.go`, add:

```go
type DockerUpdateArgs struct {
	Services []ServiceName `pos:"0+" help:"Service names"`
}
```

Replace the `docker update` command metadata with:

```go
"update": {Name: "update", Description: "Pull images and recreate containers for compose services", Usage: "docker update <svc...> | docker update --outdated", ArgsSchema: DockerUpdateArgs{}, Examples: []string{
	"yeet docker update <svc>",
	"yeet docker update <svc-a> <svc-b>",
	"yeet docker update <svc-a> <svc-b>@<host>",
	"yeet docker update --outdated",
}},
```

Replace `IsServiceArgSpec` with:

```go
func IsServiceArgSpec(spec yargs.ArgSpec) bool {
	serviceType := reflect.TypeOf(ServiceName(""))
	if spec.GoType == serviceType {
		return true
	}
	return spec.GoType == reflect.SliceOf(serviceType)
}
```

- [ ] **Step 4: Run the CLI metadata tests**

Run:

```bash
go test ./pkg/cli -run 'TestCommandRegistriesExposeMetadata|TestServiceArgSpecDetection|TestParseAdditionalCommandFlags' -count=1
```

Expected: PASS.

- [ ] **Step 5: Authorization-gated commit checkpoint**

Do not run this step unless the user explicitly authorizes commits:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go
git commit -m "pkg/cli: model docker update as variadic"
```

## Task 2: Bridge Routing

**Files:**
- Modify: `cmd/yeet/cli_bridge.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Write failing bridge tests**

Add this test to `cmd/yeet/cli_bridge_test.go`:

```go
func TestBridgeServiceArgsDockerUpdateVariadicDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "update", "svc-a", "svc-b@host-b"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected variadic docker update to stay unbridged, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	if service != "" || host != "" || bridged != nil {
		t.Fatalf("bridge result = service=%q host=%q bridged=%v, want empty", service, host, bridged)
	}
}
```

Add this case to `TestPrepareCommandRouteShortArgsAndGroupHost` in `cmd/yeet/cli_test.go`:

```go
	got = prepareCommandRoute([]string{"docker@catch-a", "update", "svc-a", "svc-b"}, "")
	if got.host != "catch-a" {
		t.Fatalf("host = %q, want catch-a", got.host)
	}
	if !reflect.DeepEqual(got.args, []string{"docker", "update", "svc-a", "svc-b"}) {
		t.Fatalf("args = %#v, want unbridged docker update services", got.args)
	}
	if got.service != "" {
		t.Fatalf("service = %q, want empty", got.service)
	}
```

Add this assertion to `TestSvcMissingServiceHelpersCoverGroupsAndEvents` in `pkg/yeet/svc_cmd_branch_test.go`:

```go
	needs, err := commandNeedsService([]string{"docker", "update", "svc-a", "svc-b"})
	if err != nil {
		t.Fatalf("commandNeedsService docker update services error: %v", err)
	}
	if needs {
		t.Fatalf("docker update with inline services needs service = true, want false")
	}
```

- [ ] **Step 2: Run the targeted failing tests**

Run:

```bash
go test ./cmd/yeet -run 'TestBridgeServiceArgsDockerUpdateVariadicDoesNotBridge|TestPrepareCommandRouteShortArgsAndGroupHost' -count=1
go test ./pkg/yeet -run TestSvcMissingServiceHelpersCoverGroupsAndEvents -count=1
```

Expected: FAIL because the bridge still strips the first docker update service and `commandNeedsService` still assumes service args arrive through the global service override.

- [ ] **Step 3: Implement variadic bridge detection**

In `cmd/yeet/cli_bridge.go`, add:

```go
func isVariadicServiceGroupCommand(group string, command string) bool {
	reg := cli.RemoteCommandRegistry()
	groupSpec, ok := reg.Groups[group]
	if !ok {
		return false
	}
	cmdSpec, ok := groupSpec.Commands[command]
	if !ok {
		return false
	}
	arg, ok := yargs.ArgSpecAt(cmdSpec.ArgsSchema, 0)
	return ok && cli.IsServiceArgSpec(arg) && arg.Variadic
}
```

Add the `github.com/shayne/yargs` import. Then update `bridgeGroupArgs` before it calls `bridgeCommandArgs`:

```go
	if isVariadicServiceGroupCommand(args[0], args[1]) {
		return "", "", nil, false
	}
```

- [ ] **Step 4: Let docker update inline services satisfy service checks**

In `pkg/yeet/svc_cmd.go`, update `commandAllowsMissingService`:

```go
func commandAllowsMissingService(args []string) (bool, error) {
	if len(args) < 2 || args[0] != "docker" || args[1] != "update" {
		return false, nil
	}
	flags, remaining, err := cli.ParseDockerUpdate(args[2:])
	if err != nil {
		return false, err
	}
	return flags.Outdated || len(remaining) > 0, nil
}
```

- [ ] **Step 5: Run bridge and service-check tests**

Run:

```bash
go test ./cmd/yeet -run 'TestBridgeServiceArgsDockerUpdateVariadicDoesNotBridge|TestBridgeServiceArgsDockerGroup|TestPrepareCommandRouteShortArgsAndGroupHost' -count=1
go test ./pkg/yeet -run 'TestSvcMissingServiceHelpersCoverGroupsAndEvents|TestCommandNeedsServiceDockerUpdateOutdated' -count=1
```

Expected: PASS.

- [ ] **Step 6: Authorization-gated commit checkpoint**

Do not run this step unless the user explicitly authorizes commits:

```bash
git add cmd/yeet/cli_bridge.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go
git commit -m "cmd/yeet: leave variadic docker update unbridged"
```

## Task 3: Docker Update Target Resolution

**Files:**
- Create: `pkg/yeet/docker_update.go`
- Create: `pkg/yeet/docker_update_test.go`

- [ ] **Step 1: Write failing target-resolution tests**

Create `pkg/yeet/docker_update_test.go` with:

```go
package yeet

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveDockerUpdateTargets(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "foo", Host: "host-a"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-b"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-c"})
	loc := &projectConfigLocation{Config: cfg}

	targets, errs := resolveDockerUpdateTargets([]string{"foo", "bar@host-d", "bar@host-d", "baz", "amb"}, loc, "", false)
	want := []dockerUpdateTarget{
		{Service: "foo", Host: "host-a"},
		{Service: "bar", Host: "host-d"},
		{Service: "baz", Host: "default-host"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "amb@host-b") || !strings.Contains(errs[0].Error(), "amb@host-c") {
		t.Fatalf("errs = %#v, want ambiguous amb error", errs)
	}
}

func TestResolveDockerUpdateTargetsHostOverrideWins(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "foo", Host: "configured-host"})
	loc := &projectConfigLocation{Config: cfg}

	targets, errs := resolveDockerUpdateTargets([]string{"foo", "bar@host-b"}, loc, "override-host", true)
	if len(errs) != 0 {
		t.Fatalf("errs = %#v, want none", errs)
	}
	want := []dockerUpdateTarget{
		{Service: "foo", Host: "override-host"},
		{Service: "bar", Host: "host-b"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}
```

- [ ] **Step 2: Run the failing resolver tests**

Run:

```bash
go test ./pkg/yeet -run 'TestResolveDockerUpdateTargets' -count=1
```

Expected: FAIL because `dockerUpdateTarget` and `resolveDockerUpdateTargets` do not exist.

- [ ] **Step 3: Implement target resolution**

Create `pkg/yeet/docker_update.go` with:

```go
package yeet

import (
	"fmt"
	"strings"
)

type dockerUpdateTarget struct {
	Service string
	Host    string
}

func dockerUpdateServicesFromRequest(remaining []string) ([]string, error) {
	if serviceOverride != "" && len(remaining) > 0 {
		return nil, fmt.Errorf("docker update takes either --service or service arguments, not both")
	}
	if len(remaining) > 0 {
		return append([]string(nil), remaining...), nil
	}
	if serviceOverride != "" {
		return []string{serviceOverride}, nil
	}
	return nil, missingServiceError([]string{"docker", "update"})
}

func resolveDockerUpdateTargets(services []string, cfgLoc *projectConfigLocation, hostOverride string, hostOverrideSet bool) ([]dockerUpdateTarget, []error) {
	targets := make([]dockerUpdateTarget, 0, len(services))
	errs := make([]error, 0)
	seen := make(map[string]struct{})
	for _, raw := range services {
		target, err := resolveDockerUpdateTarget(raw, cfgLoc, hostOverride, hostOverrideSet)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		key := target.Host + "/" + target.Service
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets, errs
}

func resolveDockerUpdateTarget(raw string, cfgLoc *projectConfigLocation, hostOverride string, hostOverrideSet bool) (dockerUpdateTarget, error) {
	raw = strings.TrimSpace(raw)
	service, host, qualified := splitServiceHost(raw)
	service = strings.TrimSpace(service)
	host = strings.TrimSpace(host)
	if service == "" {
		return dockerUpdateTarget{}, fmt.Errorf("docker update service name cannot be empty")
	}
	if qualified {
		return dockerUpdateTarget{Service: service, Host: host}, nil
	}
	if hostOverrideSet {
		return dockerUpdateTarget{Service: service, Host: hostOverride}, nil
	}
	if cfgLoc != nil && cfgLoc.Config != nil {
		resolved, err := resolveServiceHost(cfgLoc.Config, service)
		if err != nil {
			return dockerUpdateTarget{}, err
		}
		if resolved != "" {
			return dockerUpdateTarget{Service: service, Host: resolved}, nil
		}
	}
	return dockerUpdateTarget{Service: service, Host: Host()}, nil
}
```

- [ ] **Step 4: Run resolver tests**

Run:

```bash
go test ./pkg/yeet -run 'TestResolveDockerUpdateTargets' -count=1
```

Expected: PASS.

- [ ] **Step 5: Authorization-gated commit checkpoint**

Do not run this step unless the user explicitly authorizes commits:

```bash
git add pkg/yeet/docker_update.go pkg/yeet/docker_update_test.go
git commit -m "pkg/yeet: resolve docker update targets"
```

## Task 4: Explicit Multi-Service Execution

**Files:**
- Modify: `pkg/yeet/docker_update.go`
- Modify: `pkg/yeet/docker_outdated.go`
- Modify: `pkg/yeet/docker_update_test.go`
- Modify: `pkg/yeet/docker_outdated_test.go`

- [ ] **Step 1: Write failing execution tests**

Replace the import block in `pkg/yeet/docker_update_test.go`, then append the execution tests:

```go
import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDockerUpdateExplicitTargetsRunsAllAndJoinsErrors(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "foo", Host: "host-a"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-b"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-c"})
	loc := &projectConfigLocation{Config: cfg}

	var updated []string
	updateDockerServiceForHostFn = func(ctx context.Context, host string, service string) error {
		updated = append(updated, host+"/"+service)
		if service == "bad" {
			return errors.New("remote update failed")
		}
		return nil
	}

	out, err := captureSvcStdout(t, func() error {
		return dockerUpdateExplicitTargets(context.Background(), svcCommandRequest{
			Config: loc,
		}, []string{"foo", "bad@host-d", "amb", "foo"})
	})
	if err == nil {
		t.Fatal("dockerUpdateExplicitTargets error = nil, want joined errors")
	}
	if !strings.Contains(err.Error(), "remote update failed") || !strings.Contains(err.Error(), "amb@host-b") {
		t.Fatalf("joined error = %v, want remote and ambiguous errors", err)
	}
	wantUpdated := []string{"host-a/foo", "host-d/bad"}
	if !reflect.DeepEqual(updated, wantUpdated) {
		t.Fatalf("updated = %#v, want %#v", updated, wantUpdated)
	}
	for _, want := range []string{"==> host-a/foo", "==> host-d/bad"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDockerUpdateExplicitTargetSingleKeepsExistingOutput(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"

	var updated []string
	updateDockerServiceForHostFn = func(ctx context.Context, host string, service string) error {
		updated = append(updated, host+"/"+service)
		fmt.Println("streamed compose output")
		return nil
	}

	out, err := captureSvcStdout(t, func() error {
		return dockerUpdateExplicitTargets(context.Background(), svcCommandRequest{}, []string{"foo@host-a"})
	})
	if err != nil {
		t.Fatalf("dockerUpdateExplicitTargets: %v", err)
	}
	if !reflect.DeepEqual(updated, []string{"host-a/foo"}) {
		t.Fatalf("updated = %#v, want host-a/foo", updated)
	}
	if strings.Contains(out, "==>") {
		t.Fatalf("single target output should not include marker:\n%s", out)
	}
	if !strings.Contains(out, "streamed compose output") {
		t.Fatalf("streamed output missing:\n%s", out)
	}
}
```

Add this test to `pkg/yeet/docker_outdated_test.go`:

```go
func TestHandleDockerUpdateCommandRejectsServiceOverrideWithInlineServices(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	serviceOverride = "svc-a"
	err := handleDockerUpdateCommand(context.Background(), svcCommandRequest{
		Command: svcCommand{Name: "docker", Args: []string{"update", "svc-b"}, RawArgs: []string{"docker", "update", "svc-b"}},
		Service: "svc-a",
	})
	if err == nil || !strings.Contains(err.Error(), "either --service or service arguments") {
		t.Fatalf("mixed override error = %v, want either --service or service arguments", err)
	}
}
```

- [ ] **Step 2: Run the failing execution tests**

Run:

```bash
go test ./pkg/yeet -run 'TestDockerUpdateExplicit|TestHandleDockerUpdateCommandRejectsServiceOverrideWithInlineServices' -count=1
```

Expected: FAIL because `dockerUpdateExplicitTargets` does not exist and `handleDockerUpdateCommand` still forwards non-`--outdated` updates to `handleSvcRemote`.

- [ ] **Step 3: Implement explicit execution**

Extend `pkg/yeet/docker_update.go` imports:

```go
import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)
```

Add:

```go
func handleDockerUpdateCommand(ctx context.Context, req svcCommandRequest) error {
	flags, remaining, err := cli.ParseDockerUpdate(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if flags.Outdated {
		if len(remaining) > 0 || serviceOverride != "" || (req.Service != "" && req.Service != systemServiceName) {
			return fmt.Errorf("docker update --outdated does not take a service; use yeet docker update <svc> for one service")
		}
		return dockerUpdateOutdatedMultiHost(ctx, statusHosts(req.Config, req.HostOverrideSet))
	}
	services, err := dockerUpdateServicesFromRequest(remaining)
	if err != nil {
		return err
	}
	return dockerUpdateExplicitTargets(ctx, req, services)
}

func dockerUpdateExplicitTargets(ctx context.Context, req svcCommandRequest, services []string) error {
	targets, errs := resolveDockerUpdateTargets(services, req.Config, req.HostOverride, req.HostOverrideSet)
	showMarkers := len(targets) > 1
	for _, target := range targets {
		if showMarkers {
			if err := dockerUpdateOutdatedLine(os.Stdout, "==> %s/%s\n", target.Host, target.Service); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := updateDockerServiceForHostFn(ctx, target.Host, target.Service); err != nil {
			errs = append(errs, err)
			if showMarkers {
				if writeErr := dockerUpdateOutdatedLine(os.Stdout, "==> %s/%s failed: %v\n", target.Host, target.Service, err); writeErr != nil {
					errs = append(errs, writeErr)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func updateDockerServiceForHost(ctx context.Context, host string, service string) error {
	return withTemporaryHost(host, func() error {
		if err := execRemoteFn(ctx, service, []string{"docker", "update"}, nil, true); err != nil {
			return fmt.Errorf("%s/%s: %w", host, service, err)
		}
		return nil
	})
}

func withTemporaryHost(host string, fn func() error) error {
	oldPrefs := loadedPrefs
	loadedPrefs.DefaultHost = host
	defer func() {
		loadedPrefs = oldPrefs
	}()
	return fn()
}
```

Move `handleDockerUpdateCommand`, `updateDockerServiceForHost`, and `withTemporaryHost` out of `pkg/yeet/docker_outdated.go` to avoid duplicate definitions.

- [ ] **Step 4: Run execution tests**

Run:

```bash
go test ./pkg/yeet -run 'TestDockerUpdateExplicit|TestHandleDockerUpdateCommandRejectsServiceOverrideWithInlineServices|TestDockerUpdateOutdatedMultiHostUpdatesOnlyUpdateAvailable|TestUpdateDockerServiceForHostStreamsRemoteUpdateForHost' -count=1
```

Expected: PASS.

- [ ] **Step 5: Authorization-gated commit checkpoint**

Do not run this step unless the user explicitly authorizes commits:

```bash
git add pkg/yeet/docker_update.go pkg/yeet/docker_update_test.go pkg/yeet/docker_outdated.go pkg/yeet/docker_outdated_test.go
git commit -m "pkg/yeet: update multiple docker services"
```

## Task 5: Docs And Help Text

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/operations/workflows.mdx`

- [ ] **Step 1: Update README**

In `README.md`, replace the docker update sentence with:

```markdown
`yeet run --pull <svc> ./compose.yml`, `yeet docker update <svc...>`, or
`yeet docker update --outdated` to update every compose service with available
image updates. Explicit updates may mix hosts with `yeet docker update foo
bar@catch-b baz`; unqualified services still use `yeet.toml` or the default
catch host. Batch updates print a short host/service marker, then stream the
same output as `yeet docker update <svc>`.
```

- [ ] **Step 2: Update website CLI manual**

In `website/docs/cli/yeet-cli.mdx`, replace the docker update examples and note with:

````markdown
```bash
yeet docker update <svc>
yeet docker update <svc-a> <svc-b>
yeet docker update <svc-a> <svc-b>@<host>
yeet docker update --outdated
yeet --host=yeet-prod docker update --outdated
```

Explicit service arguments update each requested compose service. A target like
`svc@host` pins that service to one host; unqualified services use `--host`, a
single matching `yeet.toml` service host, or the default catch host. When more
than one target is updated, each update prints a short host/service marker
before streaming the normal compose update output.

`--outdated` is a host-wide batch mode: it checks compose services for update
rows, deduplicates by service, and updates only services with available image
updates. It does not take service arguments; use explicit services for selected
updates.
```
````

- [ ] **Step 3: Update workflow docs**

In `website/docs/operations/workflows.mdx`, replace examples mentioning `yeet docker update <svc>` with `yeet docker update <svc...>` and add this sentence near the compose refresh paragraph:

```markdown
Use `svc@host` for individual targets that should run on a specific host, such
as `yeet docker update web api@catch-b worker`.
```

- [ ] **Step 4: Run docs whitespace checks**

Run:

```bash
git diff --check -- README.md website/docs/cli/yeet-cli.mdx website/docs/operations/workflows.mdx
```

Expected: PASS with no output.

- [ ] **Step 5: Authorization-gated commit checkpoint**

Do not run this step unless the user explicitly authorizes commits:

```bash
git add README.md website/docs/cli/yeet-cli.mdx website/docs/operations/workflows.mdx
git commit -m "docs: document multi-service docker update"
```

## Task 6: Final Verification

**Files:**
- Verify all touched Go and docs files.

- [ ] **Step 1: Format Go files**

Run:

```bash
gofmt -w pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go pkg/yeet/docker_update.go pkg/yeet/docker_update_test.go pkg/yeet/docker_outdated.go pkg/yeet/docker_outdated_test.go
```

Expected: command exits 0.

- [ ] **Step 2: Run targeted package tests**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 3: Run service helper tests for Docker regressions**

Run:

```bash
go test ./pkg/catch ./pkg/svc -count=1
```

Expected: PASS.

- [ ] **Step 4: Run full Go suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 5: Run docs diff check**

Run:

```bash
git diff --check
```

Expected: PASS with no output.

- [ ] **Step 6: Authorization-gated final commit checkpoint**

Do not run this step unless the user explicitly authorizes commits:

```bash
git status --short
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go pkg/yeet/docker_update.go pkg/yeet/docker_update_test.go pkg/yeet/docker_outdated.go pkg/yeet/docker_outdated_test.go README.md website/docs/cli/yeet-cli.mdx website/docs/operations/workflows.mdx docs/superpowers/specs/2026-05-18-docker-update-multiple-services-design.md docs/superpowers/plans/2026-05-19-docker-update-multiple-services.md
git commit -m "pkg/yeet: support multi-service docker update"
```

Expected: commit succeeds only after explicit user authorization.
