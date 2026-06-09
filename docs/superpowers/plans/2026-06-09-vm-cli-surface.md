# VM CLI Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move VM resource mutation from `yeet service set` to `yeet vm set` while keeping `yeet run` as the create/update entry point for every payload type.

**Architecture:** Add a dedicated `cli.VMSetFlags` parser and expose `vm set <vm>` in shared command metadata. Catch should route `vm set` through the existing VM resize/rewire planner, while `service set` keeps only root, published-port, and snapshot settings. The client should persist `vm set` changes back into VM `yeet.toml` entries using the same stored run-arg rewrite behavior that VM flags use today.

**Tech Stack:** Go, yargs command metadata, catch TTY command routing, yeet local config persistence, website MDX docs.

---

## File Structure

- Modify `pkg/cli/cli.go`: define `VMSetFlags`, add `vm set` command metadata and parser, and remove VM-only flags from `ServiceSetFlags`.
- Modify `pkg/cli/cli_test.go`: cover `ParseVMSet`, reject old VM flags from `service set`, and assert registry metadata.
- Modify `cmd/yeet/cli.go`: add `vm set` to the VM group handler.
- Modify `cmd/yeet/cli_bridge_test.go` and `cmd/yeet/cli_test.go`: assert service-arg bridging and help use `vm set`.
- Modify `pkg/catch/tty_vm.go`: parse and execute `vm set`.
- Modify `pkg/catch/tty_service_set.go`: remove VM changes from `service set`.
- Modify `pkg/catch/vm_resize.go`: accept `cli.VMSetFlags` instead of `cli.ServiceSetFlags`.
- Modify `pkg/catch/vm_resize_test.go` and `pkg/catch/tty_service_set_test.go`: update resize tests and add service-set regression coverage.
- Modify `pkg/yeet/svc_cmd.go`: route `vm set` locally, execute remote command, and persist VM run-arg changes.
- Modify `pkg/yeet/svc_cmd_branch_test.go`: move VM config persistence tests from `service set` to `vm set`.
- Modify `website/docs/**/*.mdx`, `README.md` if needed, and `.codex/skills/yeet-cli/references/yeet-help-llm.md`: update public examples and generated CLI help.

## Task 1: Shared CLI Parser and Metadata

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`

- [ ] **Step 1: Write failing parser tests**

Add `TestParseVMSetFlags` to `pkg/cli/cli_test.go`:

```go
func TestParseVMSetFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    VMSetFlags
		wantOut []string
		wantErr string
	}{
		{
			name:    "shape flags",
			args:    []string{"devbox", "--cpus=8", "--memory", "8g", "--disk=128g"},
			want:    VMSetFlags{CPUs: 8, Memory: "8g", Disk: "128g"},
			wantOut: []string{"devbox"},
		},
		{
			name: "network flags",
			args: []string{"--net", "svc,lan", "--macvlan-parent=vmbr0", "--macvlan-vlan=42", "--macvlan-mac=02:00:00:00:00:42", "devbox"},
			want: VMSetFlags{
				Net:           "svc,lan",
				NetworkChange: true,
				MacvlanParent: "vmbr0",
				MacvlanVlan:   42,
				MacvlanMac:    "02:00:00:00:00:42",
			},
			wantOut: []string{"devbox"},
		},
		{name: "missing change", args: []string{"devbox"}, wantErr: "vm set requires settings to change"},
		{name: "negative cpus", args: []string{"devbox", "--cpus=-1"}, wantErr: "VM CPU count must be positive"},
		{name: "negative vlan", args: []string{"devbox", "--macvlan-vlan=-1"}, wantErr: "--macvlan-vlan must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, out, err := ParseVMSet(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseVMSet error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVMSet error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("args = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}
```

Update `TestParseServiceSetFlags` by removing the VM shape/network success cases and adding:

```go
{name: "rejects vm shape flags", args: []string{"svc-a", "--cpus=8"}, wantErr: "unknown flag"},
{name: "rejects vm network flags", args: []string{"svc-a", "--net=lan"}, wantErr: "unknown flag"},
```

- [ ] **Step 2: Run parser tests to verify failure**

Run:

```bash
go test ./pkg/cli -count=1
```

Expected: fail because `ParseVMSet`, `VMSetFlags`, and `vm set` metadata do not exist yet, and because `service set` still accepts VM flags.

- [ ] **Step 3: Implement VM parser and metadata**

In `pkg/cli/cli.go`, add:

```go
type VMSetFlags struct {
	CPUs          int
	Memory        string
	Disk          string
	Net           string
	NetworkChange bool
	MacvlanMac    string
	MacvlanVlan   int
	MacvlanParent string
}

type vmSetFlagsParsed struct {
	CPUs          int    `flag:"cpus"`
	Memory        string `flag:"memory"`
	Disk          string `flag:"disk"`
	Net           string `flag:"net"`
	MacvlanMac    string `flag:"macvlan-mac"`
	MacvlanVlan   int    `flag:"macvlan-vlan"`
	MacvlanParent string `flag:"macvlan-parent"`
}
```

Remove these fields from `ServiceSetFlags` and `serviceSetFlagsParsed`: `CPUs`, `Memory`, `Disk`, `Net`, `NetworkChange`, `MacvlanMac`, `MacvlanVlan`, and `MacvlanParent`.

Add `vm set` metadata under the existing `vm` group:

```go
"set": {
	Name:        "set",
	Description: "Set VM resources and networking",
	Usage:       "vm set <vm> [--cpus=N] [--memory=SIZE] [--disk=SIZE] [--net=svc|lan|svc,lan] [--macvlan-parent=IFACE] [--macvlan-vlan=ID] [--macvlan-mac=MAC]",
	Examples: []string{
		"yeet vm set <vm> --cpus=8 --memory=8g --disk=128g",
		"yeet vm set <vm> --net=lan",
		"yeet vm set <vm> --net=svc,lan --macvlan-parent=vmbr0 --macvlan-vlan=4",
	},
	ArgsSchema: ServiceArgs{},
},
```

Add `"set": flagSpecsFromStruct(vmSetFlagsParsed{})` to `remoteGroupFlagSpecs["vm"]`.

Add:

```go
func ParseVMSet(args []string) (VMSetFlags, []string, error) {
	specs := remoteGroupFlagSpecs["vm"]["set"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[vmSetFlagsParsed](parseArgs)
	if err != nil {
		return VMSetFlags{}, nil, err
	}
	flags := VMSetFlags{
		CPUs:          parsed.Flags.CPUs,
		Memory:        strings.TrimSpace(parsed.Flags.Memory),
		Disk:          strings.TrimSpace(parsed.Flags.Disk),
		Net:           strings.TrimSpace(parsed.Flags.Net),
		NetworkChange: hasNamedFlag(parseArgs, "--net"),
		MacvlanMac:    strings.TrimSpace(parsed.Flags.MacvlanMac),
		MacvlanVlan:   parsed.Flags.MacvlanVlan,
		MacvlanParent: strings.TrimSpace(parsed.Flags.MacvlanParent),
	}
	if err := validateVMSetFlags(flags); err != nil {
		return VMSetFlags{}, nil, err
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func validateVMSetFlags(flags VMSetFlags) error {
	if flags.CPUs < 0 {
		return fmt.Errorf("VM CPU count must be positive")
	}
	if flags.MacvlanVlan < 0 {
		return fmt.Errorf("--macvlan-vlan must not be negative")
	}
	if !hasVMSetChange(flags) {
		return fmt.Errorf("vm set requires settings to change")
	}
	return nil
}

func hasVMSetChange(flags VMSetFlags) bool {
	return flags.CPUs != 0 ||
		strings.TrimSpace(flags.Memory) != "" ||
		strings.TrimSpace(flags.Disk) != "" ||
		flags.NetworkChange ||
		strings.TrimSpace(flags.MacvlanMac) != "" ||
		flags.MacvlanVlan != 0 ||
		strings.TrimSpace(flags.MacvlanParent) != ""
}
```

Update service-set validation so it only checks root, publish, migration, and snapshot fields.

- [ ] **Step 4: Update registry tests**

In `TestRemoteCommandRegistryAndFlagSpecs`, change service-set usage to:

```go
"service set <svc> [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit] [--snapshot-keep-last=N] [--snapshot-max-age=7d] [--snapshot-events=run,docker-update] [--snapshot-required=true|false]"
```

Remove service-set VM examples and assert `vm set` exists:

```go
if reg.Groups["vm"].Commands["set"].Info.Usage != "vm set <vm> [--cpus=N] [--memory=SIZE] [--disk=SIZE] [--net=svc|lan|svc,lan] [--macvlan-parent=IFACE] [--macvlan-vlan=ID] [--macvlan-mac=MAC]" {
	t.Fatalf("vm set usage = %q", reg.Groups["vm"].Commands["set"].Info.Usage)
}
```

Move VM flag consume assertions from `service set` to `vm set`:

```go
for _, flag := range []string{"--cpus", "--memory", "--disk", "--net", "--macvlan-parent", "--macvlan-vlan", "--macvlan-mac"} {
	if !RemoteGroupFlagSpecs()["vm"]["set"][flag].ConsumesValue {
		t.Fatalf("vm set %s should consume a value", flag)
	}
	if _, ok := RemoteGroupFlagSpecs()["service"]["set"][flag]; ok {
		t.Fatalf("service set %s should not be registered", flag)
	}
}
```

- [ ] **Step 5: Verify parser and registry tests**

Run:

```bash
go test ./pkg/cli -count=1
```

Expected: pass.

## Task 2: Client Command Routing and Local Config Persistence

**Files:**
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Write failing bridge and config tests**

In `cmd/yeet/cli_bridge_test.go`, replace the VM cases in `TestBridgeServiceArgsServiceSet` with a new `TestBridgeServiceArgsVMSet`:

```go
func TestBridgeServiceArgsVMSet(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	tests := []struct {
		name        string
		args        []string
		wantService string
		wantHost    string
		wantBridged string
	}{
		{
			name:        "vm shape flags after service",
			args:        []string{"vm", "set", "devbox", "--cpus=8", "--memory", "8g", "--disk=128g"},
			wantService: "devbox",
			wantBridged: "vm set --cpus=8 --memory 8g --disk=128g",
		},
		{
			name:        "vm net flags before service",
			args:        []string{"vm", "set", "--net", "lan", "--macvlan-parent=vmbr0", "devbox"},
			wantService: "devbox",
			wantBridged: "vm set --net lan --macvlan-parent=vmbr0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, host, bridged, ok := bridgeServiceArgs(tt.args, remoteSpecs, groupSpecs, "")
			if !ok {
				t.Fatal("ok = false, want true")
			}
			if service != tt.wantService || host != tt.wantHost {
				t.Fatalf("service/host = %q/%q, want %q/%q", service, host, tt.wantService, tt.wantHost)
			}
			if got := strings.Join(bridged, " "); got != tt.wantBridged {
				t.Fatalf("bridged args = %q, want %q", got, tt.wantBridged)
			}
		})
	}
}
```

In `pkg/yeet/svc_cmd_branch_test.go`, rename `TestServiceSetVMFlagsUpdateStoredRunArgs` to `TestVMSetFlagsUpdateStoredRunArgs` and change the command and expected remote args:

```go
if err := HandleSvcCmd([]string{"vm", "set", "--cpus=8", "--memory", "8g", "--disk=128g", "--net", "lan", "--macvlan-parent=vmbr0"}); err != nil {
	t.Fatalf("HandleSvcCmd: %v", err)
}
if !reflect.DeepEqual(gotArgs, []string{"vm", "set", "--cpus=8", "--memory", "8g", "--disk=128g", "--net", "lan", "--macvlan-parent=vmbr0"}) {
	t.Fatalf("remote args = %#v", gotArgs)
}
```

Change `TestServiceSetVMFlagsDoNotUpdateNonVMRunArgs` to assert `vm set --cpus=8` leaves a non-VM config entry unchanged after the remote call succeeds.

- [ ] **Step 2: Run routing tests to verify failure**

Run:

```bash
go test ./cmd/yeet ./pkg/yeet -count=1
```

Expected: fail because `vm set` is not registered in the VM group and `handleSvcVM` does not persist VM set config.

- [ ] **Step 3: Implement VM routing**

Add `"set": handleVMGroup` in `cmd/yeet/cli.go` under the `vm` group.

In `pkg/yeet/svc_cmd.go`, update `handleSvcVM`:

```go
func handleSvcVM(ctx context.Context, req svcCommandRequest) error {
	args := req.Command.RawArgs
	if len(args) >= 2 && args[0] == "vm" && args[1] == "images" {
		flags, remaining, err := cli.ParseVMImages(args[2:])
		if err != nil {
			return err
		}
		if len(remaining) > 0 && remaining[0] == "import" {
			return handleVMImagesImportParsed(ctx, flags, remaining)
		}
		return handleSvcRemote(ctx, req)
	}
	if len(args) >= 2 && args[0] == "vm" && args[1] == "set" {
		return handleVMSet(ctx, req)
	}
	return handleSvcRemote(ctx, req)
}
```

Add:

```go
func handleVMSet(ctx context.Context, req svcCommandRequest) error {
	flags, _, err := cli.ParseVMSet(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if err := execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, false); err != nil {
		return err
	}
	updated, err := saveVMSetConfig(req.Config, req.HostOverride, flags)
	if err != nil {
		return fmt.Errorf("updated catch VM settings, but failed to update %s: %w", projectConfigName, err)
	}
	if !updated {
		return printServiceSetSyncHint(os.Stdout, req.Service, serviceSetSyncHintHost(req))
	}
	return nil
}
```

Add:

```go
func saveVMSetConfig(cfgLoc *projectConfigLocation, hostOverride string, flags cli.VMSetFlags) (bool, error) {
	if serviceOverride == "" {
		return false, nil
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return false, nil
	}
	applyVMSetConfigFlags(&entry, flags)
	cfgLoc.Config.SetServiceEntry(entry)
	return true, saveProjectConfig(cfgLoc)
}

func applyVMSetConfigFlags(entry *ServiceEntry, flags cli.VMSetFlags) {
	removals, updates := vmSetRunFlagChanges(flags)
	if len(removals) == 0 || !serviceEntryIsVM(*entry) {
		return
	}
	entry.Args = rewriteStoredRunArgs(entry.Args, removals, updates)
}
```

Rename `serviceSetVMRunFlagChanges` to `vmSetRunFlagChanges` and make it accept `cli.VMSetFlags`. Remove the call to `applyServiceSetVMConfigFlags` from `applyServiceSetConfigFlags`.

- [ ] **Step 4: Verify routing tests**

Run:

```bash
go test ./cmd/yeet ./pkg/yeet -count=1
```

Expected: pass.

## Task 3: Catch-Side VM Set Execution

**Files:**
- Modify: `pkg/catch/tty_vm.go`
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_resize_test.go`
- Modify: `pkg/catch/tty_service_set_test.go`

- [ ] **Step 1: Write failing catch tests**

Update `pkg/catch/vm_resize_test.go` to use `cli.VMSetFlags` in every `updateVMServiceSettings` call. Rename test names from `TestServiceSetVM...` to `TestVMSet...`.

Add to `pkg/catch/tty_service_set_test.go`:

```go
func TestServiceSetRejectsVMFlags(t *testing.T) {
	execer := newTestTTYExecer(t)
	err := execer.serviceCmdFunc([]string{"set", "--cpus=8"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("service set --cpus error = %v, want unknown flag", err)
	}
}
```

Add a VM command dispatch test in `pkg/catch/vm_resize_test.go` or an existing VM TTY test file:

```go
func TestVMCmdSetUpdatesShape(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	execer := &ttyExecer{s: server, sn: "devbox", rw: io.Discard}

	if err := execer.vmCmdFunc([]string{"set", "--cpus=6", "--memory=6g"}); err != nil {
		t.Fatalf("vm set: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.CPUs != 6 || svc.VM.MemoryBytes != 6<<30 {
		t.Fatalf("vm shape = %d/%d, want 6/%d", svc.VM.CPUs, svc.VM.MemoryBytes, int64(6<<30))
	}
}
```

- [ ] **Step 2: Run catch tests to verify failure**

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: fail because `updateVMServiceSettings` still accepts `ServiceSetFlags`, `service set` still handles VM flags, and `vm set` is not routed.

- [ ] **Step 3: Implement catch routing and type split**

In `pkg/catch/tty_vm.go`, add:

```go
case "set":
	flags, rest, err := cli.ParseVMSet(args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected vm set args: %s", strings.Join(rest, " "))
	}
	return e.s.updateVMServiceSettings(e.vmProvisionContext(), e.sn, flags)
```

In `pkg/catch/vm_resize.go`, change all VM settings functions to accept `cli.VMSetFlags`:

```go
func (s *Server) updateVMServiceSettings(ctx context.Context, name string, flags cli.VMSetFlags) error
func (s *Server) planVMServiceSettings(name string, flags cli.VMSetFlags) (vmSettingsPlan, error)
func (s *Server) applyVMShapeSettings(service *db.Service, flags cli.VMSetFlags, plan *vmSettingsPlan) error
func applyVMDiskSettings(flags cli.VMSetFlags, plan *vmSettingsPlan) error
func (s *Server) applyVMNetworkSettings(dv *db.DataView, name string, service *db.Service, flags cli.VMSetFlags, plan *vmSettingsPlan) error
func hasCatchVMSetChange(flags cli.VMSetFlags) bool
func hasCatchVMSetNetworkChange(flags cli.VMSetFlags) bool
func (s *Server) planVMServiceSetNetwork(..., flags cli.VMSetFlags) ...
func vmNetworkValueForServiceSet(current []db.VMNetworkConfig, flags cli.VMSetFlags) string
func vmNetworkInputForServiceSet(svcNet *db.SvcNetwork, modes []string, flags cli.VMSetFlags) (vmNetworkInputs, error)
```

In `pkg/catch/tty_service_set.go`, remove the `vm` field from `serviceSetChanges`, remove `hasCatchServiceSetVMChange(flags)`, and remove the `updateVMServiceSettings` call.

Update the no-change error to:

```go
return fmt.Errorf("service set requires --service-root, snapshot settings, or published ports")
```

- [ ] **Step 4: Verify catch tests**

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: pass.

## Task 4: Public Docs and Generated CLI Help

**Files:**
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/concepts/zfs.mdx`
- Modify: `website/docs/changelog.mdx`
- Modify if matched: `README.md`
- Modify generated: `.codex/skills/yeet-cli/references/yeet-help-llm.md`

- [ ] **Step 1: Replace VM service-set examples**

Run:

```bash
rg -n "yeet service set <vm>|yeet service set devbox --cpus|yeet service set devbox --net|service set <vm> --disk|service set` can change CPU|VM resource updates" README.md website/docs
```

Make these replacements:

```md
yeet service set <vm> --cpus=6 --memory=6g
yeet service set <vm> --disk=128g
yeet service set <vm> --net=lan
```

becomes:

```md
yeet vm set <vm> --cpus=6 --memory=6g
yeet vm set <vm> --disk=128g
yeet vm set <vm> --net=lan
```

Change prose that says "`yeet service set` can change CPU count, memory..." to "`yeet vm set` can change CPU count, memory...".

Add a changelog bullet under the current unreleased/latest section:

```md
- Moved VM resource and networking changes to `yeet vm set`, keeping `yeet service set` focused on service roots, published ports, and snapshots.
```

- [ ] **Step 2: Regenerate CLI help reference**

Run:

```bash
mise exec -- tools/generate-yeet-help-llm.sh
```

Expected: `.codex/skills/yeet-cli/references/yeet-help-llm.md` updates `service set` help and adds `vm set` help.

- [ ] **Step 3: Verify docs do not mention old VM mutation surface**

Run:

```bash
rg -n "yeet service set <vm>|yeet service set devbox --cpus|yeet service set devbox --net|service set` can change CPU|service set <vm> --disk" README.md website/docs .codex/skills/yeet-cli/references/yeet-help-llm.md
```

Expected: no matches.

Run:

```bash
git -C website diff --check
```

Expected: no whitespace errors.

## Task 5: Full Verification

**Files:**
- No new files. Verify all touched code and docs.

- [ ] **Step 1: Run targeted command help checks**

Run:

```bash
go run ./cmd/yeet vm set --help
go run ./cmd/yeet service set --help
```

Expected:
- `vm set` help shows CPU, memory, disk, network, and macvlan flags.
- `service set` help shows service root, publish, and snapshot flags only.

- [ ] **Step 2: Run targeted Go tests**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
```

Expected: pass.

- [ ] **Step 3: Run full Go suite**

Run:

```bash
go test ./... -count=1
```

Expected: pass.

- [ ] **Step 4: Review diff**

Run:

```bash
git diff --stat
git diff -- pkg/cli/cli.go pkg/catch/tty_vm.go pkg/catch/tty_service_set.go pkg/catch/vm_resize.go pkg/yeet/svc_cmd.go
```

Expected:
- `ServiceSetFlags` no longer contains VM-only fields.
- `vm set` is present in shared CLI metadata and client group handlers.
- Catch handles VM changes only through `vm set`.
- Local VM run args are rewritten only from `vm set`.

- [ ] **Step 5: Commit only after authorization**

Repository policy requires explicit user authorization before committing. If the user authorizes commits for this run, use:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go pkg/catch/tty_vm.go pkg/catch/tty_service_set.go pkg/catch/vm_resize.go pkg/catch/vm_resize_test.go pkg/catch/tty_service_set_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go website/docs/payloads/vms.mdx website/docs/operations/workflows.mdx website/docs/cli/yeet-cli.mdx website/docs/concepts/zfs.mdx website/docs/changelog.mdx .codex/skills/yeet-cli/references/yeet-help-llm.md docs/superpowers/plans/2026-06-09-vm-cli-surface.md README.md
git commit -m "vm: move resource updates to vm set"
```

If `README.md` is unchanged, omit it from `git add`.
