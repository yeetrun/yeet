# Firecracker Nested-Virtualization Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every newly rendered Yeet Firecracker VM explicitly hide Intel VMX and AMD SVM, deliberately backfill the directly managed VM set, and prepare a safe backfill for an operator-owned VM service.

**Architecture:** Keep Firecracker's host-specific CPU model and add only a top-level custom `cpu-config` containing two one-bit CPUID masks. Enforce the policy at the shared Firecracker renderer so provisioning, resize, and kernel rewrites converge automatically, while existing running VMs are restarted only through explicit one-at-a-time operational backfills.

**Tech Stack:** Go 1.25 via `mise`, Firecracker v1.16.1 configuration JSON, Bash, `jq`, systemd, GitButler, GitHub CLI.

## Global Constraints

- Do not use Firecracker static CPU templates.
- Match the [Firecracker v1.16.1 API schema](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/src/firecracker/swagger/firecracker.yaml#L1054-L1127) exactly: CPUID leaf and subleaf are strings, flags are a 32-bit integer, and register modifiers use 32-bit bitmap strings.
- Clear only Intel VMX at CPUID leaf `0x1`, subleaf `0x0`, ECX bit 5.
- Clear only AMD SVM at CPUID leaf `0x80000001`, subleaf `0x0`, ECX bit 2.
- Do not add a CLI flag, RPC method, permission mapping, automatic migration, or automatic VM restart.
- Treat CPUID masking as defense in depth; host KVM still requires the vendor fix for CVE-2026-64561.
- Preserve unrelated GitButler branches, uncommitted changes, generated test artifacts, and other agents' live state.
- Do not cut a release, push, or upgrade Catch unless the user separately authorizes that publication step.
- Treat `root@vm-host.example`, `yeet-vm-host`, `deferred-vm-service`, and
  `vm-guest.example` as public placeholders. Replace them with authorized
  operator-supplied values before running any command.

---

## File Structure

- Modify `pkg/catch/vm_firecracker.go`: own the typed Firecracker CPU-config schema, the exact nested-virtualization policy, and central render-time enforcement.
- Modify `pkg/catch/vm_firecracker_test.go`: prove the exact two masks and the absence of broad/static template configuration.
- Modify `pkg/catch/vm_provision_test.go`: prove a newly provisioned VM receives the policy.
- Modify `pkg/catch/vm_resize_test.go`: prove a legacy config gains the policy during a settings rewrite.
- Modify `pkg/catch/vm_kernel_sync_test.go`: prove a kernel rewrite preserves or adds the policy.
- Modify `pkg/catch/vm_runtime_adoption_test.go`: prove strict adoption accepts both legacy and hardened JSON and fuzzes the expanded config reader.
- Create only as a temporary operational artifact: `/tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh`. Publish the validated content as a pinned GitHub gist; do not add it to the repository.

### Task 1: Build and Validate the One-VM Backfill Helper

**Files:**
- Create temporarily: `/tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh`
- Test temporarily: `/tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh`

**Interfaces:**
- Consumes: one explicit Yeet VM service name and the corresponding `yeet-vm-${service}.service` systemd unit.
- Produces: `harden-firecracker-nesting.sh --check NAME` for read-only inspection and `harden-firecracker-nesting.sh --apply NAME` for atomic backfill, restart, verification, and rollback.

- [ ] **Step 1: Create the helper with `apply_patch`**

Create the temporary directory with `mkdir -p /tmp/yeet-firecracker-nesting-20260809`, then use `apply_patch` to write this exact script and make it executable with `chmod 0755`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  echo "usage: $0 --check|--apply SERVICE" >&2
  exit 64
}

[[ $# -eq 2 ]] || usage
mode=$1
service=$2
[[ $mode == --check || $mode == --apply ]] || usage
[[ $service =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]] || {
  echo "invalid service name: $service" >&2
  exit 64
}
[[ $EUID -eq 0 ]] || {
  echo "run as root" >&2
  exit 77
}

for command in jq systemctl mktemp cp mv chown chmod rm date dirname sleep uname; do
  command -v "$command" >/dev/null || {
    echo "required command is missing: $command" >&2
    exit 69
  }
done

unit="yeet-vm-${service}.service"
systemctl cat "$unit" >/dev/null
active_state=$(systemctl is-active "$unit" || true)
[[ $active_state == active ]] || {
  echo "$unit is not active: $active_state" >&2
  exit 1
}
main_pid=$(systemctl show --property MainPID --value "$unit")
[[ $main_pid =~ ^[1-9][0-9]*$ && -r /proc/$main_pid/cmdline ]] || {
  echo "$unit has no readable running vm-run process" >&2
  exit 1
}

argv=()
while IFS= read -r -d '' arg; do
  argv+=("$arg")
done <"/proc/$main_pid/cmdline"

config_paths=()
for ((i = 0; i < ${#argv[@]}; i++)); do
  case ${argv[$i]} in
    --config-file)
      ((i + 1 < ${#argv[@]})) || {
        echo "$unit has a dangling --config-file argument" >&2
        exit 1
      }
      config_paths+=("${argv[$((i + 1))]}")
      ;;
    --config-file=*)
      config_paths+=("${argv[$i]#--config-file=}")
      ;;
  esac
done

[[ ${#config_paths[@]} -eq 1 ]] || {
  echo "$unit exposed ${#config_paths[@]} config paths; expected exactly one" >&2
  exit 1
}
config_path=${config_paths[0]}
[[ $config_path = /* && -f $config_path && ! -L $config_path ]] || {
  echo "unsafe Firecracker config path: $config_path" >&2
  exit 1
}

expected=$(jq -cnS '{
  cpuid_modifiers: [
    {
      leaf: "0x1",
      subleaf: "0x0",
      flags: 0,
      modifiers: [
        {register: "ecx", bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx"}
      ]
    },
    {
      leaf: "0x80000001",
      subleaf: "0x0",
      flags: 0,
      modifiers: [
        {register: "ecx", bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx"}
      ]
    }
  ]
}')
current=$(jq -cS '."cpu-config" // null' "$config_path")

printf 'unit=%s\nactive_state=%s\npid=%s\nconfig=%s\nhost_kernel=%s\ncpu_config=%s\n' \
  "$unit" "$active_state" "$main_pid" "$config_path" "$(uname -r)" "$current"

if [[ $current != null && $current != "$expected" ]]; then
  echo "unexpected existing cpu-config; refusing to overwrite" >&2
  exit 1
fi
if [[ $mode == --check ]]; then
  exit 0
fi
if [[ $current == "$expected" ]]; then
  echo "$service is already explicitly hardened"
  exit 0
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup="${config_path}.pre-cve-2026-64561-${stamp}"
tmp=$(mktemp "$(dirname "$config_path")/.firecracker.json.XXXXXX")
restore_tmp=""
cleanup() {
  rm -f "$tmp"
  [[ -z $restore_tmp ]] || rm -f "$restore_tmp"
}
trap cleanup EXIT

cp --preserve=mode,ownership,timestamps "$config_path" "$backup"
jq --argjson expected "$expected" '."cpu-config" = $expected' "$config_path" >"$tmp"
jq -e '."cpu-config".cpuid_modifiers | length == 2' "$tmp" >/dev/null
chown --reference="$config_path" "$tmp"
chmod --reference="$config_path" "$tmp"
mv -f "$tmp" "$config_path"

restore() {
  restore_tmp=$(mktemp "$(dirname "$config_path")/.firecracker-restore.json.XXXXXX")
  cp --preserve=mode,ownership,timestamps "$backup" "$restore_tmp"
  mv -f "$restore_tmp" "$config_path"
  restore_tmp=""
  systemctl restart "$unit" || true
}

if ! systemctl restart "$unit"; then
  echo "restart failed; restoring $backup" >&2
  restore
  exit 1
fi

new_pid=""
for _ in {1..30}; do
  candidate=$(systemctl show --property MainPID --value "$unit")
  if systemctl is-active --quiet "$unit" &&
    [[ $candidate =~ ^[1-9][0-9]*$ ]] &&
    [[ $candidate != "$main_pid" ]]; then
    new_pid=$candidate
    break
  fi
  sleep 1
done

if [[ -z $new_pid ]]; then
  echo "the restarted unit did not become active with a new PID; restoring $backup" >&2
  restore
  exit 1
fi

applied=$(jq -cS '."cpu-config"' "$config_path")
if [[ $applied != "$expected" ]]; then
  echo "post-restart cpu-config verification failed; restoring $backup" >&2
  restore
  exit 1
fi

printf 'hardened=%s\nold_pid=%s\nnew_pid=%s\nbackup=%s\n' \
  "$service" "$main_pid" "$new_pid" "$backup"
```

- [ ] **Step 2: Check syntax and the exact policy**

Run:

```bash
bash -n /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh
rg -n '0x1|0x80000001|xxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx|xxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx' \
  /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh
```

Expected: `bash -n` exits `0`; the search shows exactly the two leaves and two bitmaps.

- [ ] **Step 3: Verify argument failure behavior locally**

Run:

```bash
/tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh --check 'bad/service/name'
```

Expected: exit `64` with `invalid service name` before any root or systemd check and with no filesystem mutation.

- [ ] **Step 4: Review the script before any live mutation**

Read the whole script, confirm it targets one explicit service, discovers the config from the running unit's `MainPID`, rejects symlinks and unexpected CPU policies, uses same-directory atomic writes, and restores the backup after any restart or verification failure.

### Task 2: Backfill the Directly Managed VM Set

**Files:**
- Read and update remotely: each discovered VM's active `firecracker.json` under its service root.
- Copy remotely: `/tmp/harden-firecracker-nesting.sh` on the placeholder host
  `root@vm-host.example`.

**Interfaces:**
- Consumes: the helper from Task 1, placeholder host
  `root@vm-host.example`, placeholder Catch alias `yeet-vm-host`, and the
  operator-approved active `catch vm-run` inventory.
- Produces: an explicitly hardened running VM set, independent configuration
  backups, and per-VM before/after evidence.

- [ ] **Step 1: Reconfirm the target host and enumerate the exact active VM set**

Run:

```bash
ssh root@vm-host.example 'uname -n; uname -r; for p in /sys/module/kvm_intel/parameters/nested /sys/module/kvm_amd/parameters/nested; do test ! -r "$p" || printf "%s=%s\n" "$p" "$(cat "$p")"; done; ps -eo args' \
  | tee /tmp/yeet-firecracker-nesting-20260809/vm-host-preflight.txt
awk '/[/]catch .* vm-run / {
  for (i = 1; i <= NF; i++) {
    if ($i == "--service" && i < NF) print $(i + 1)
  }
}' /tmp/yeet-firecracker-nesting-20260809/vm-host-preflight.txt \
  | sort -u \
  > /tmp/yeet-firecracker-nesting-20260809/vm-host-services.txt
expected_vm_count='<operator-approved-count>'
[[ $expected_vm_count =~ ^[1-9][0-9]*$ ]] || {
  echo 'replace <operator-approved-count> before running' >&2
  exit 64
}
actual_vm_count=$(wc -l < /tmp/yeet-firecracker-nesting-20260809/vm-host-services.txt)
[[ $actual_vm_count -eq $expected_vm_count ]]
```

Expected: hostname and KVM state match the intended VM-capable host, and the
inventory contains exactly the operator-approved number of active VM services.
Stop if the count differs.

- [ ] **Step 2: Copy the reviewed helper and check every discovered VM**

Run:

```bash
scp /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh \
  root@vm-host.example:/tmp/harden-firecracker-nesting.sh
ssh root@vm-host.example 'chmod 0755 /tmp/harden-firecracker-nesting.sh'
if ssh root@vm-host.example '/tmp/harden-firecracker-nesting.sh --check missing-vm-service'; then
  echo 'missing-unit check unexpectedly succeeded' >&2
  exit 1
fi
mkdir -p /tmp/yeet-firecracker-nesting-20260809/guest-health
while IFS= read -r service; do
  ssh root@vm-host.example "/tmp/harden-firecracker-nesting.sh --check $service"
  CATCH_HOST=yeet-vm-host yeet ssh "$service" -- sh -c \
    'test -z "$(grep -m1 "^flags" /proc/cpuinfo | grep -Ewo "vmx|svm" || true)"; test ! -e /dev/kvm'
  CATCH_HOST=yeet-vm-host yeet ssh "$service" -- sh -c \
    'systemctl --failed --no-legend --plain --no-pager | LC_ALL=C sort' \
    > "/tmp/yeet-firecracker-nesting-20260809/guest-health/${service}.before"
done < /tmp/yeet-firecracker-nesting-20260809/vm-host-services.txt
```

Expected: the deliberate missing-unit check fails without mutation. Every active config reports either `cpu_config=null` or the exact two-modifier policy; any other value stops the rollout. Every guest lacks VMX/SVM and `/dev/kvm`; the per-service files record any pre-existing failed guest units before proceeding.

- [ ] **Step 3: Apply and verify one VM at a time**

Run this loop without parallelism:

```bash
while IFS= read -r service; do
  CATCH_HOST=yeet-vm-host yeet info "$service"
  ssh root@vm-host.example "/tmp/harden-firecracker-nesting.sh --apply $service"
  CATCH_HOST=yeet-vm-host yeet ssh "$service" -- sh -c \
    'test -z "$(grep -m1 "^flags" /proc/cpuinfo | grep -Ewo "vmx|svm" || true)"; test ! -e /dev/kvm'
  CATCH_HOST=yeet-vm-host yeet ssh "$service" -- sh -c \
    'systemctl --failed --no-legend --plain --no-pager | LC_ALL=C sort' \
    > "/tmp/yeet-firecracker-nesting-20260809/guest-health/${service}.after"
  diff -u \
    "/tmp/yeet-firecracker-nesting-20260809/guest-health/${service}.before" \
    "/tmp/yeet-firecracker-nesting-20260809/guest-health/${service}.after"
  CATCH_HOST=yeet-vm-host yeet info "$service"
done < /tmp/yeet-firecracker-nesting-20260809/vm-host-services.txt
```

Expected: each helper invocation reports a new PID and backup; each guest check succeeds; failed-unit inventories are unchanged; before/after `yeet info` remains consistent. Stop on the first failure and investigate the helper's automatic rollback before touching the next VM.

- [ ] **Step 4: Re-read all configs and record host-kernel status**

Run:

```bash
while IFS= read -r service; do
  ssh root@vm-host.example "/tmp/harden-firecracker-nesting.sh --check $service"
done < /tmp/yeet-firecracker-nesting-20260809/vm-host-services.txt
ssh root@vm-host.example 'uname -r'
```

Expected: every managed config reports the exact hardened policy. Record the
host kernel as a separate CVE remediation item; require a vendor update and
reboot if the operator has not already applied the fix.

### Task 3: Add Central Catch Enforcement with TDD

**Files:**
- Modify: `pkg/catch/vm_firecracker.go:9-55`
- Modify: `pkg/catch/vm_firecracker_test.go:7-46`
- Modify: `pkg/catch/vm_provision_test.go:867-955`
- Modify: `pkg/catch/vm_resize_test.go:69-85`
- Modify: `pkg/catch/vm_kernel_sync_test.go:442-469`
- Modify: `pkg/catch/vm_runtime_adoption_test.go:24-70,699-715,836-844`

**Interfaces:**
- Produces: `firecrackerNestedVirtualizationDisabledCPUConfig() firecrackerCPUConfig`; `firecrackerConfig.CPUConfig firecrackerCPUConfig`; exact JSON key `cpu-config`.
- Consumes: the existing `renderFirecrackerConfig(firecrackerConfig) ([]byte, error)` choke point used by provisioning and rewrite paths.

- [ ] **Step 1: Write the failing renderer test**

Add `reflect` to `vm_firecracker_test.go` and add this independent expected value so the test catches a wrong helper implementation:

```go
func TestRenderFirecrackerConfigDisablesNestedVirtualization(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource:    firecrackerBootSource{KernelImagePath: "/vmlinux"},
		Drives:        []firecrackerDrive{{DriveID: "rootfs", PathOnHost: "/rootfs", IsRootDevice: true}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 1024},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	var got firecrackerConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	want := firecrackerCPUConfig{CPUIDModifiers: []firecrackerCPUIDLeafModifier{
		{
			Leaf: "0x1", Subleaf: "0x0", Flags: 0,
			Modifiers: []firecrackerCPUIDRegisterModifier{{
				Register: "ecx", Bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx",
			}},
		},
		{
			Leaf: "0x80000001", Subleaf: "0x0", Flags: 0,
			Modifiers: []firecrackerCPUIDRegisterModifier{{
				Register: "ecx", Bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx",
			}},
		},
	}}
	if !reflect.DeepEqual(got.CPUConfig, want) {
		t.Fatalf("CPUConfig = %#v, want %#v", got.CPUConfig, want)
	}
	text := string(raw)
	if strings.Contains(text, "cpu_template") || strings.Contains(text, "msr_modifiers") {
		t.Fatalf("config contains broad CPU-template state:\n%s", raw)
	}
}
```

- [ ] **Step 2: Add failing lifecycle assertions**

In the named existing success tests, define the path once and assert the generated file contains all three strings:

```go
firecrackerPath := filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json")
for _, want := range []string{
	`"cpu-config"`,
	`"bitmap": "0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx"`,
	`"bitmap": "0bxxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx"`,
} {
	assertFileContains(t, firecrackerPath, want)
}
```

For `TestRunVMProvisionSuccessWritesArtifactsAndDB`, use the snippet exactly as shown. For `TestVMSetUpdatesShapeAndFirecrackerConfig` and `TestVMKernelSyncUpdatesFirecrackerConfigAndDB`, use their existing `root` variables when constructing `firecrackerPath`; the kernel-sync test already defines it. Add the assertion loop in:

- `TestRunVMProvisionSuccessWritesArtifactsAndDB`;
- `TestVMSetUpdatesShapeAndFirecrackerConfig`; and
- `TestVMKernelSyncUpdatesFirecrackerConfigAndDB`.

For runtime adoption, keep `TestVMRuntimeAdoptionInventoryMeasuresEffectiveLaunchComposition` as the legacy no-`cpu-config` acceptance case. Add this second test:

```go
func TestVMRuntimeAdoptionInventoryAcceptsHardenedCPUConfig(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	configPath := filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), "firecracker.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Firecracker config: %v", err)
	}
	var config firecrackerConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode Firecracker config: %v", err)
	}
	config.CPUConfig = firecrackerNestedVirtualizationDisabledCPUConfig()
	writeVMRuntimeAdoptionTestJSON(t, configPath, config, 0o644)

	vm := fixture.onlyVM(t)
	if vm.BlockedReason != "" {
		t.Fatalf("hardened config blocked adoption: %s", vm.BlockedReason)
	}
}
```

- [ ] **Step 3: Add a failing fuzz target for the expanded strict config reader**

Add:

```go
func FuzzDecodeVMRuntimeAdoptionFirecrackerConfig(f *testing.F) {
	f.Add(`{"boot-source":{"kernel_image_path":"/vmlinux"},"drives":[],"network-interfaces":[],"machine-config":{"vcpu_count":1,"mem_size_mib":256}}`)
	f.Add(`{"boot-source":{"kernel_image_path":"/vmlinux"},"drives":[],"network-interfaces":[],"machine-config":{"vcpu_count":1,"mem_size_mib":256},"cpu-config":{"cpuid_modifiers":[{"leaf":"0x1","subleaf":"0x0","flags":0,"modifiers":[{"register":"ecx","bitmap":"0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx"}]}]}}`)
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = decodeVMRuntimeAdoptionFirecrackerConfig([]byte(input))
	})
}
```

- [ ] **Step 4: Run focused tests and confirm the expected compile failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRenderFirecrackerConfigDisablesNestedVirtualization|TestRunVMProvisionSuccessWritesArtifactsAndDB|TestVMSetUpdatesShapeAndFirecrackerConfig|TestVMKernelSyncUpdatesFirecrackerConfigAndDB|TestVMRuntimeAdoptionInventoryAcceptsHardenedCPUConfig' -count=1
```

Expected: FAIL because `CPUConfig`, `firecrackerCPUConfig`, and `firecrackerNestedVirtualizationDisabledCPUConfig` do not exist yet.

- [ ] **Step 5: Implement the minimal typed policy and renderer enforcement**

Add the field to `firecrackerConfig`:

```go
CPUConfig firecrackerCPUConfig `json:"cpu-config"`
```

Add these types and helper near `firecrackerMachineConfig`:

```go
type firecrackerCPUConfig struct {
	CPUIDModifiers []firecrackerCPUIDLeafModifier `json:"cpuid_modifiers"`
}

type firecrackerCPUIDLeafModifier struct {
	Leaf      string                              `json:"leaf"`
	Subleaf   string                              `json:"subleaf"`
	Flags     uint32                              `json:"flags"`
	Modifiers []firecrackerCPUIDRegisterModifier `json:"modifiers"`
}

type firecrackerCPUIDRegisterModifier struct {
	Register string `json:"register"`
	Bitmap   string `json:"bitmap"`
}

func firecrackerNestedVirtualizationDisabledCPUConfig() firecrackerCPUConfig {
	return firecrackerCPUConfig{CPUIDModifiers: []firecrackerCPUIDLeafModifier{
		{
			Leaf: "0x1", Subleaf: "0x0", Flags: 0,
			Modifiers: []firecrackerCPUIDRegisterModifier{{
				Register: "ecx", Bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx",
			}},
		},
		{
			Leaf: "0x80000001", Subleaf: "0x0", Flags: 0,
			Modifiers: []firecrackerCPUIDRegisterModifier{{
				Register: "ecx", Bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx",
			}},
		},
	}}
}
```

Change the renderer to enforce the policy centrally:

```go
func renderFirecrackerConfig(cfg firecrackerConfig) ([]byte, error) {
	cfg.CPUConfig = firecrackerNestedVirtualizationDisabledCPUConfig()
	return json.MarshalIndent(cfg, "", "  ")
}
```

- [ ] **Step 6: Format and run the focused tests**

Run:

```bash
mise exec -- gofmt -w \
  pkg/catch/vm_firecracker.go \
  pkg/catch/vm_firecracker_test.go \
  pkg/catch/vm_provision_test.go \
  pkg/catch/vm_resize_test.go \
  pkg/catch/vm_kernel_sync_test.go \
  pkg/catch/vm_runtime_adoption_test.go
mise exec -- go test ./pkg/catch -run 'TestRenderFirecrackerConfigDisablesNestedVirtualization|TestRunVMProvisionSuccessWritesArtifactsAndDB|TestVMSetUpdatesShapeAndFirecrackerConfig|TestVMKernelSyncUpdatesFirecrackerConfigAndDB|TestVMRuntimeAdoptionInventoryAcceptsHardenedCPUConfig' -count=1
mise exec -- go test ./pkg/catch -run '^FuzzDecodeVMRuntimeAdoptionFirecrackerConfig$' -count=1
```

Expected: all focused tests and the fuzz seed corpus pass.

- [ ] **Step 7: Review the code diff against the exact policy**

Run `but diff`. Confirm the code diff touches only the six listed Go files, contains exactly two CPUID modifiers, contains no static `cpu_template`, and does not include unrelated workspace or generated-test files.

### Task 4: Run the Repository Gates, Record Vendor Coverage, and Commit the Implementation

**Files:**
- Verify: all files modified in Task 3.
- Preserve: every unrelated file and branch reported by GitButler.

**Interfaces:**
- Consumes: the complete Task 3 implementation.
- Produces: one coherent local implementation commit on `codex/zapscape-hardening`, plus an explicit Intel/AMD validation record, with no push or release.

- [ ] **Step 1: Run the package and full test suites**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
mise exec -- go test ./...
```

Expected: PASS. If generated `.vm-runtime-adoption-test-*` artifacts appear, attribute them to the creating process before cleanup; never delete another active task's artifacts.

- [ ] **Step 2: Run the deterministic repository gate once**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS. Fix only findings caused by this branch; report unrelated pre-existing failures separately.

- [ ] **Step 3: Record real-vendor integration coverage**

Run:

```bash
ssh root@vm-host.example 'lscpu | sed -n "/^Vendor ID:/p"'
```

Expected: the direct rollout supplies integration evidence for the vendor
reported by the placeholder VM host. Inspect other authorized, reachable
VM-capable hosts for the other x86 vendor. If a suitable host is unavailable,
record that vendor's live validation as unavailable rather than inferring it
from the first result. If the user later supplies a suitable KVM host, repeat
Task 1 against one disposable stopped/restartable VM there, then verify that
the guest retains its normal ISA flags while exposing neither `vmx`/`svm` nor
`/dev/kvm`.

- [ ] **Step 4: Check the base and commit only the hardening files**

Run:

```bash
but pull --check
but diff
```

Use the exact file or hunk IDs printed by `but diff` for the six hardening files, excluding every unrelated ID. Stage each selected ID to `codex/zapscape-hardening` with `but stage`, inspect `but status` to prove only those changes are assigned there, then run:

```bash
but commit codex/zapscape-hardening -m "vm: disable nested virtualization" --only
```

Expected: one new implementation commit on top of the design and plan commits; unrelated changes remain uncommitted or assigned to their existing branches. Do not push.

### Task 5: Publish the Validated Operator Helper as a Pinned Gist

**Files:**
- Read: `/tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh`
- External write: one unlisted GitHub gist containing `harden-firecracker-nesting.sh`.

**Interfaces:**
- Consumes: the helper validated on the directly managed VM set and explicit user authorization to use GitHub gists.
- Produces: immutable raw gist URL, SHA-256, and exact operator commands for
  the placeholder `deferred-vm-service`.

- [ ] **Step 1: Revalidate the exact script being published**

Run:

```bash
bash -n /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh
shasum -a 256 /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh
```

Expected: syntax passes and a stable SHA-256 is recorded.

- [ ] **Step 2: Create an unlisted gist and resolve its immutable raw URL**

Run:

```bash
gist_url=$(gh gist create \
  /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh \
  --desc 'Harden one existing Yeet Firecracker VM against nested virtualization')
gist_id=${gist_url##*/}
raw_url=$(gh api "gists/$gist_id" \
  --jq '.files["harden-firecracker-nesting.sh"].raw_url')
revision=$(gh api "gists/$gist_id" --jq '.history[0].version')
printf 'gist=%s\nrevision=%s\nraw=%s\n' "$gist_url" "$revision" "$raw_url"
case $raw_url in
  */"$revision"/*) ;;
  *)
    github_login=$(gh api user --jq .login)
    raw_url="https://gist.githubusercontent.com/${github_login}/${gist_id}/raw/${revision}/harden-firecracker-nesting.sh"
    ;;
esac
printf '%s\n' "$raw_url" \
  > /tmp/yeet-firecracker-nesting-20260809/immutable-raw-url.txt
```

Expected: the raw URL contains the immutable gist revision. If it does not, construct the raw URL with the returned gist ID, revision, and filename before sharing it.

- [ ] **Step 3: Verify the published bytes and compute the operator checksum**

Run:

```bash
raw_url=$(< /tmp/yeet-firecracker-nesting-20260809/immutable-raw-url.txt)
curl -fsSL "$raw_url" -o /tmp/yeet-firecracker-nesting-20260809/published.sh
cmp \
  /tmp/yeet-firecracker-nesting-20260809/harden-firecracker-nesting.sh \
  /tmp/yeet-firecracker-nesting-20260809/published.sh
operator_sha256=$(shasum -a 256 \
  /tmp/yeet-firecracker-nesting-20260809/published.sh | awk '{print $1}')
printf 'sha256=%s\n' "$operator_sha256"
printf '%s\n' "$operator_sha256" \
  > /tmp/yeet-firecracker-nesting-20260809/operator-sha256.txt
```

Expected: `cmp` exits `0`; retain the immutable raw URL and checksum.

- [ ] **Step 4: Materialize the exact post-release operator handoff**

Reload the recorded values and materialize a command file with the resolved immutable URL and checksum:

```bash
raw_url=$(< /tmp/yeet-firecracker-nesting-20260809/immutable-raw-url.txt)
operator_sha256=$(< /tmp/yeet-firecracker-nesting-20260809/operator-sha256.txt)
cat > /tmp/yeet-firecracker-nesting-20260809/operator-commands.txt <<EOF
yeet upgrade
curl -fsSLo harden-firecracker-nesting.sh '$raw_url'
printf '%s\n' '$operator_sha256  harden-firecracker-nesting.sh' | sha256sum -c -
sudo bash harden-firecracker-nesting.sh --check deferred-vm-service
sudo bash harden-firecracker-nesting.sh --apply deferred-vm-service
EOF
sed -n '1,5p' /tmp/yeet-firecracker-nesting-20260809/operator-commands.txt
```

Expected: the file contains a literal revision-pinned URL and a literal
64-character SHA-256; it contains no variable references. Tell the operator to
replace `deferred-vm-service` with the authorized service name, run
`yeet upgrade` from their configured Yeet client, and run the remaining
commands on the Catch host that owns that service.

Also instruct the operator to install the host vendor's CVE-2026-64561 kernel update and reboot. Do not invent distribution-specific commands until the operator reports the host OS and package source.

- [ ] **Step 5: Record the deferred guest verification**

After the operator reports success, run:

```bash
ssh vm-guest.example \
  'test -z "$(grep -m1 "^flags" /proc/cpuinfo | grep -Ewo "vmx|svm" || true)"; test ! -e /dev/kvm; systemctl --failed --no-pager'
```

Expected: after replacing `vm-guest.example` with the operator-supplied guest
endpoint, VMX/SVM are absent, `/dev/kvm` is absent, and there are no new failed
units. This verification is explicitly deferred until after the separately
authorized release and operator action.

### Task 6: Final Non-Release Handoff

**Files:**
- Read: the committed design, plan, code commits, live evidence, and gist metadata.

**Interfaces:**
- Consumes: Tasks 1-5.
- Produces: a precise status separating live backfill, committed repository work, gist readiness, deferred remote-operator work, host-kernel state, and release state.

- [ ] **Step 1: Recheck the directly managed configs after repository work**

Run the helper's `--check` mode against the names retained in
`/tmp/yeet-firecracker-nesting-20260809/vm-host-services.txt` and repeat the
guest VMX/SVM and `/dev/kvm` checks. This catches any intervening legacy Catch
rewrite.

- [ ] **Step 2: Report the exact completion boundary**

Report separately:

- directly managed VM set: backfilled and live-verified, or the exact failed
  target and rollback state;
- repository: committed locally on `codex/zapscape-hardening`, not pushed or landed;
- operator helper: immutable gist URL and checksum ready;
- operator-owned VM service: explicit host-side backfill and guest verification
  deferred;
- host kernels: patched/rebooted or still outstanding per host;
- release: not created, not tagged, and not published.

Do not claim full remediation until the patched Catch release is installed,
the deferred VM service is backfilled, and every VM-capable host has booted a
vendor kernel containing the KVM fix.
