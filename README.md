# yeet

Deploy containers, VMs, binaries, scripts, and cron jobs from your workstation to Linux hosts.

The normal way to run a little infrastructure is to accidentally build a platform. You start with SSH, add some shell scripts, decide you need a deploy box, invent a secrets story, bolt on a dashboard, and eventually discover that the dashboard is mostly a slower way to run SSH.

We built `yeet` because we didn't want any of that.

You run `yeet` on your workstation. It installs a small daemon called `catch` on a Linux host, then sends commands to it over Tailscale. Catch turns those commands into boring Linux things you can still inspect when something goes wrong: systemd units, Docker Compose projects, containers, cron jobs, files, and VMs.

There isn't a control plane hiding behind the curtain. There are just fewer places for state to hide.

<p>
  <a href="https://yeetrun.com"><strong>yeetrun.com</strong></a>
  · <a href="https://yeetrun.com/docs/getting-started/quick-start">Quick Start</a>
  · <a href="https://yeetrun.com/docs/getting-started/host-setup">Host Setup</a>
  · <a href="https://yeetrun.com/docs">Docs</a>
</p>

## What yeet is for

We use yeet for the awkward middle ground between "we can SSH in and run this" and "apparently we operate a miniature cloud provider now." If you have one or more Linux hosts and want to run real services on them, that's the territory.

Yeet can deploy:

- Docker Compose stacks
- Container images
- Local Dockerfiles
- Linux binaries
- Shell scripts
- Cron jobs
- Linux VMs on KVM-capable hosts

Yeet fits single-operator homelabs and small private infrastructure, and it expects Linux hosts with systemd. New native binaries, scripts, and cron jobs run as the unprivileged `yeet-svc` account by default. Docker and VM identities stay in their own runtimes, where they belong.

This is for hosts you control. It is not a multi-tenant platform, and we don't want the convenience of the tool to suggest otherwise.

## The model

Yeet is deliberately two pieces:

- `yeet`: the CLI on your workstation.
- `catch`: the daemon on each Linux host you manage.

The first setup uses SSH because something has to get Catch onto the machine:

```bash
yeet init root@<machine-host>
```

After that, normal commands target Catch over Tailscale:

```bash
yeet status
yeet run <svc> ./compose.yml
yeet logs -f <svc>
```

That distinction matters. Use the machine hostname for `yeet init`, then use the Catch hostname for normal yeet commands. The default Catch hostname is `catch`.

## Quick start

This is the shortest honest path from nothing to a disposable container. It sets up the trust boundary first, installs Catch, and only then runs a workload.

### 1. Install yeet locally

```bash
curl -fsSL https://yeetrun.com/install.sh | sh
```

If you want the nightly build instead:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh -s -- --nightly
```

Make sure the CLI is there:

```bash
yeet --help
```

### 2. Prepare Tailscale

Catch joins your tailnet as a tagged device, usually `tag:catch`. It rejects user-owned Catch nodes; the daemon is infrastructure, not somebody's laptop wearing a convincing hostname.

You need a Tailscale OAuth client secret. In the Tailscale admin console, go to:

```text
Trust credentials -> Credential -> OAuth
```

For the first install, broad access is the simple path:

```text
All - Read & Write
```

The tighter path is Auth Keys write access for the tag Catch will use, usually `tag:catch`.

Your tailnet policy also needs to allow the setup user to reach catch on TCP `41548` with the `yeetrun.com/app/yeet` app permissions:

- `read`
- `manage`
- `ssh`

First setup needs all three. Once the host works, split them if you want narrower roles; debugging permissions while the daemon does not exist yet is an especially dull way to spend an afternoon.

### 3. Install catch on a host

```bash
yeet init root@<machine-host>
```

If you SSH as a non-root user, yeet runs the remote install with sudo.

Interactive setup asks for the Tailscale OAuth client secret. Catch stores its state here by default:

```text
/var/lib/yeet
```

By default, the service root is `<data-dir>/services`, which makes it `/var/lib/yeet/services` with the default data directory. If the host needs a different filesystem path, set `--data-dir` or `--services-root` during init. Yeet preserves explicit custom roots during upgrades and guided migrations; choosing a real storage layout should not become a temporary suggestion on the next upgrade.

If Docker is missing on a Debian/Ubuntu-style host, interactive setup asks before installing it. If the host can run VMs, setup can ask about VM tools too.

If the host has ZFS and you want service data on datasets:

```bash
yeet init --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services root@<machine-host>
```

Rerunning `yeet init` upgrades Catch without changing explicit custom or ZFS roots. When an interactive upgrade finds the exact legacy home-directory layout, it can offer to move that state to `/var/lib/yeet`. If init cannot prompt, run the same migration explicitly:

```bash
yeet host set \
  --data-dir=/var/lib/yeet \
  --services-root=/var/lib/yeet/services \
  --migrate-services=all \
  --yes
yeet host cleanup --from=/root/yeet-data --yes
```

`yeet host set` moves and validates the active state, but it deliberately leaves the old tree alone. Cleanup is separate because "the new copy looks good" and "delete the old copy" are not the same decision. Cleanup refuses arbitrary paths, revalidates the active Catch and service state, and removes only the journaled inactive source. If deletion alone fails, rerun the same cleanup command to resume it safely.

ZFS datasets are not copied or deleted implicitly. Dataset-backed data and nested datasets stay put until you manage them explicitly.

### 4. Confirm the host works

```bash
yeet version
yeet status
```

If you have more than one Catch host:

```bash
yeet --host=<catch-host> status
```

Save a default:

```bash
yeet config --host=<catch-host>
```

### 5. Create a service workspace

Yeet writes `yeet.toml` after a successful deploy, so put services in a directory you mean to keep. Temporary directories always feel permanent right up until they make their point.

```bash
mkdir -p ~/yeet-services
cd ~/yeet-services
```

After setup, yeet can remember this workspace in `$XDG_CONFIG_HOME/yeet/config.toml`, which lets commands from other directories find the right `yeet.toml`. If the current directory already has a `yeet.toml`, interactive commands such as `yeet status` can offer to adopt it as the saved workspace.

That file is the local bit of state that makes a command elsewhere behave as if you ran it from the workspace. Nothing more mysterious than that.

### 6. Run something disposable

```bash
yeet run -p 18080:80 hello nginx:alpine
yeet status hello
yeet logs hello
```

Make Catch prove it can reach the published port:

```bash
yeet ssh -- curl -fsS http://127.0.0.1:18080/ >/dev/null
```

Remove it:

```bash
yeet rm --clean hello
```

Read the prompt. `--clean` means what it says: it deletes service data, including VM disks for VM services, and removes the local `yeet.toml` entry.

## Common deploys

Once Catch exists, most deploys collapse to one command. Run these from a service workspace so yeet has somewhere sensible to remember what you did.

### Guided deploy

```bash
yeet run --web
yeet run --web <svc>
yeet run --web <svc> ./compose.yml
```

### Compose

```bash
yeet run <svc> ./compose.yml
```

### Container image

```bash
yeet run -p 8080:80 <svc> nginx:alpine
```

### Dockerfile

```bash
yeet run <svc> ./Dockerfile
```

### Local image

```bash
yeet docker push <svc> <local-image>:<tag> --run
```

### Binary

```bash
GOOS=linux GOARCH=amd64 go build -o ./bin/<svc> ./cmd/<svc>
yeet run <svc> ./bin/<svc>
```

### Script

```bash
yeet run <svc> ./script.sh -- --app-flag value
```

### Native sandboxing

Native processes are where "just run this binary" quietly becomes "let this binary see the host." Fresh native binaries, shebang scripts, and scheduled jobs therefore run through Bubblewrap by default. Existing native services stay in the `legacy` state until you choose `on` or `off` for each one:

```bash
yeet service set api --sandbox=on
yeet service set api --sandbox=off
```

`legacy` describes what happened before the choice existed; it is not a value accepted by `--sandbox`. `--sandbox=off` is the explicit escape hatch. That choice is independent of `--run-as=root` and the selected network mode because filesystem visibility, process identity, and networking are different boundaries.

The default sandbox mounts the service data directory read-write, then mounts the payload and required host runtime files read-only. `/tmp` and `/run` are private. `/root`, `/home`, `/var`, `/sys`, and other services simply are not there unless the fixed runtime policy or an explicit exposure requires them.

Use `--sandbox-ro` to expose another read-only file or directory, and `--sandbox-rw` for a writable directory. Both flags accept `SOURCE` or `SOURCE:DEST`, and both can be repeated:

```bash
yeet run api ./api --sandbox-ro=/etc/api --sandbox-rw=/srv/api-cache:/cache
```

For an existing service, a mentioned read-only or writable list is the complete desired list for that access class. Catch refuses to guess that an omitted entry should disappear. Preserve the current entries while adding another, or use the class-specific `reset` token when you really mean to replace the list:

```bash
yeet service set api --sandbox-ro=/etc/api --sandbox-ro=/etc/ssl
yeet service set api --sandbox-ro=reset --sandbox-ro=/etc/api
```

An exposure-only `service set` command changes an `off` service to `on`. To edit dormant exposures while keeping direct execution, repeat the state in the same command:

```bash
yeet service set api --sandbox=off --sandbox-ro=/etc/api
```

Sandboxed workloads get new user, PID, IPC, and UTS namespaces while inheriting the network mode and systemd cgroup Yeet already selected. That sharply limits filesystem and process visibility. It does not turn a process into a VM. A root workload still shares the host kernel, so an escape still has host-root consequences.

Catch installs and probes Bubblewrap for a fresh Catch installation, or when a new or changed native service ends up with sandbox state `on`. On compatible Ubuntu hosts where AppArmor restricts unprivileged user namespaces, Catch also installs and loads the exact Yeet-owned profile at `/etc/apparmor.d/yeet-bwrap`, then repeats the probe as a non-root user. Debian and hosts without that restriction need only the Bubblewrap package. Catch never disables AppArmor or changes a host-wide user-namespace sysctl. If the file at the managed path differs from Yeet's profile, Catch preserves it and blocks activation with recovery guidance instead of winning the argument by overwriting it.

Ordinary Yeet or Catch upgrades do not install the dependency, and neither do services that remain `legacy` or explicitly `off`. An exposure-only edit of an `off` service results in `on`, so it also runs the dependency readiness work; include `--sandbox=off` in that edit if you only want to prepare dormant exposures. The [native sandboxing guide](https://yeetrun.com/docs/concepts/native-sandboxing) has the complete policy and troubleshooting steps.

### Scheduled job

```bash
yeet run backup ./backup --cron="0 3 * * *" --run-as=backup --net=iso -- --full
```

Scheduling is the same native deployment model with a clock attached. It works for native binaries and shebang scripts, and scheduled runs deploy or redeploy the payload with native service options such as `--run-as`, `--net=iso`, environment files, custom service roots, ZFS, snapshots, and payload arguments after `--`.

If you rerun a scheduled service without `--cron`, yeet preserves the installed schedule. A new non-empty `--cron` value replaces it. A scheduled name does not silently become an ordinary service: remove it with `yeet rm`, then recreate it without `--cron`.

Change only the schedule of an installed scheduled native service without a
payload:

```bash
yeet service set backup --cron="30 2 * * *"
```

`service set --cron` works only for an already scheduled native binary or script. It does not convert an ordinary, container, or VM service into a scheduled service, cannot clear a schedule or combine with another service mutation, and leaves the server-side payload and other settings alone. After Catch updates the schedule, yeet updates a matching `yeet.toml`. If the local config is absent or cannot be saved, `yeet service sync <svc>` pulls reality back into the workspace.

Native binaries, scripts, and scheduled jobs run as the managed `yeet-svc` system account by default. If the workload genuinely needs an existing host identity, choose it with `--run-as=USER[:GROUP]`:

```bash
yeet run <svc> ./bin/<svc> --run-as=app:app
```

Docker execution identities stay in Compose (`user:`), while VM host execution uses the separate `yeet-vm` jailer account. Use `service set` to change a native service identity:

```bash
yeet service set <svc> --run-as=yeet-svc
yeet service set <svc> \
  --service-root=/var/lib/yeet/services/<svc> \
  --copy \
  --run-as=yeet-svc
```

This stops the native workload, verifies the service root, updates ownership and systemd definitions as one rollback-safe transaction, then restores the prior running state. ZFS-backed roots stay on their configured dataset. Non-root native workloads cannot request privileged host ports below 1024, so use a higher host port or keep that workload explicitly root-owned.

Custom service roots must live below host-controlled directories. Every parent must be owned by root and must not be group- or world-writable. `/srv/apps` and ZFS mountpoints are typical choices; a workload-owned home directory is rejected because the workload could replace paths while Catch is operating on them. For an operator-created account, systemd also applies that account's configured supplementary groups, so review memberships such as `docker` before selecting it. `yeet ssh <svc>` deliberately clears supplementary groups to give the service shell a narrower view.

### VM

```bash
yeet vm images catalog
yeet run <vm> vm://ubuntu/26.04
yeet ssh <vm>
```

VMs add a boundary that a native sandbox cannot: a separate kernel. Yeet launches Firecracker through the matching Firecracker jailer. Catch prepares the VM's host resources as root, then the jailer runs the VMM as the static, non-login `yeet-vm` host account. That host account is separate from both the VM guest login user and native-service `--run-as` identities.

Yeet creates `yeet-vm` automatically during the first VM preparation, or during an upgrade that finds VMs. Custom data roots, custom service roots, and ZFS-backed VM storage continue to work because Yeet derives their paths from stored configuration instead of assuming the default layout.

The host Firecracker and jailer pair has its own lifecycle. It is not the guest root filesystem, guest packages, guest kernel, or guest login user, even though an incautious "upgrade the VM" can make those layers sound like one thing. See what each VM has running, configured, staged, and available for rollback:

```bash
yeet vm runtime status
yeet vm runtime status <vm> --format=json-pretty
```

Runtime policy is manual by default. `yeet vm runtime update` refreshes the host runtime cache without staging or restarting a VM. `upgrade` stages an exact runtime for the next start. Add `--restart` only when downtime is acceptable:

```bash
yeet vm runtime update
yeet vm runtime upgrade <vm>
yeet vm runtime upgrade <vm> --restart
yeet vm runtime rollback <vm> --restart
```

A guest package upgrade cannot request a host runtime change. A normal guest reboot can consume a runtime that an operator or host policy already staged, but it cannot select or download one. Catch upgrades leave running VMs alone too. The optional `stage-on-restart` policy stages promoted releases without restarting VMs. Guest activity crosses that boundary only after an operator or host policy has placed something on the other side of it.

Create and restore a VM disk recovery point on a ZFS-backed VM:

```bash
yeet snapshots create <vm> --comment "before package upgrade"
yeet snapshots restore <vm> <snapshot> --stop --start --yes
```

For a running VM, Catch pauses the guest, takes one atomic ZFS snapshot of the disk, then resumes it. The result is crash-consistent disk state, not guest memory or VMM runtime state. Raw-disk VMs cannot be snapshotted, and restore replaces only the VM disk state.

Service names created by `yeet run` must use lowercase letters, numbers, and dashes, start with a letter, and end with a letter or number. Boring names survive shell scripts.

After a deploy succeeds, rerun the saved service with:

```bash
yeet run <svc>
```

## Operating services

Status:

```bash
yeet status
yeet status <svc>
yeet status <svc-a> <svc-b>
yeet info
yeet info <svc>
```

Logs:

```bash
yeet logs <svc>
yeet logs -f <svc>
```

Shells and commands:

```bash
yeet ssh
yeet ssh <svc>
yeet ssh -- uname -a
yeet ssh <svc> -- ls -la
```

After `yeet init`, host and regular service shells go through Catch over Tailscale. They do not need the original host SSH key or host password. VM services are different: they still connect to the guest operating system with SSH keys.

Lifecycle:

```bash
yeet restart <svc>
yeet stop <svc>
yeet start <svc>
yeet rm <svc>
```

`yeet rm <svc>` keeps service data by default and prompts before removing the local config entry. Add `--clean` only when you mean to remove the data too.

If a native `--net=iso` service is quarantined, `start` and `restart` leave it stopped and preserve the recorded diagnostic. That is intentional: automatically retrying a workload after its isolation boundary failed would turn a loud failure into a quiet policy change. Correct the reported failure, then inspect the service before recovering the quarantined record manually on the Catch host:

```bash
yeet info <svc>
```

`yeet stop <svc>` does not clear quarantine. Recovery requires an operator to verify the isolation boundary and runtime before clearing the record by hand.

## Targeting hosts

Use `root@<machine-host>` for `yeet init`. After installation, use Catch hostnames for normal commands:

```bash
CATCH_HOST=<catch-host> yeet status
yeet --host=<catch-host> status
yeet status@<catch-host>
yeet run <svc>@<catch-host> ./compose.yml
```

For a second Catch host, choose a distinct Catch hostname during setup:

```bash
yeet --host=morpheus-catch init root@<machine-host>
```

Save the default:

```bash
yeet config --host=<catch-host>
```

## Networking

Networking gets confusing when a mode is described only by who can connect to it. DNS and outbound traffic move too, sometimes through a different gateway, so Yeet makes the whole choice explicit:

- `--net=svc`: private service network, yeet DNS, normal outbound internet through the catch host.
- `--net=svc,ts`: `svc` behavior plus a service-owned Tailscale identity. Use this for most Tailscale-exposed services.
- `--net=lan`: LAN or VLAN address. Outbound internet comes from that network's DHCP gateway.
- `--net=ts`: tailnet-only unless you configure a Tailscale exit node.
- `--net=iso`: stable private address with public IPv4 egress, public-only DNS,
  and no workload-initiated access to catch, LAN, `svc`, Tailscale, or other
  isolated projects. Catch can still connect to the workload on any port.
- `--net=iso,ts`: `iso` behavior plus a service-owned Tailscale identity for
  supported container-backed payloads.

Choose network flags on `yeet run` when you first deploy a service. For an existing non-VM service, change the network through `service set`:

```bash
yeet service set <svc> --net=iso
yeet service set <svc> --net=ts --ts-tags=tag:app
yeet service set <svc> --net=host
yeet service set <svc> --ts-exit=
```

`--net` replaces the complete mode set. Other supplied network flags patch one setting, and an explicit empty value clears an optional setting. Any result that includes `ts` must keep at least one Tailscale tag. The mutation restarts the service immediately. If Catch changes the live service but the local config cannot be saved, `yeet service sync <svc>` makes the workspace agree with the host again.

Rerunning `yeet run` can still update a payload or unrelated configuration, but it rejects network drift for an existing service and points you to `service set`. VM network changes stay under `vm set`; stop the VM before changing it:

```bash
yeet stop <vm>
yeet vm set <vm> --net=lan
yeet vm set <vm> --net=svc,lan --macvlan-parent=vmbr0
yeet start <vm>
```

VM `--net=lan` attaches the guest TAP to a host bridge. On supported Debian/Ubuntu hosts, yeet can prepare `br0` during `yeet init` or before the first VM LAN create.

The `iso` mode supports VMs, native binaries and scripts, timer-backed jobs, and supported container payloads. Native and timer workloads get the same isolated networking whether they run as root or another account. The mode does not change identity or privilege policy, and we do not claim that it contains a hostile host-root process. VMs use `iso` alone and can install Tailscale inside the guest when needed. Isolated networking also rejects published ports and unsafe Compose features.

Read the networking docs before combining modes on a real service. Future you is still the person who has to debug it.

## Storage

ZFS is optional. Really.

If you use a ZFS services root, yeet treats it as a dataset prefix. Services below it get child datasets, which is what makes snapshots and fast VM disk clones possible.

That is persistent storage, and persistent storage has an excellent memory for casual decisions. Read the ZFS docs first if the data matters.

## Upgrades

Check the local yeet CLI and your Catch hosts:

```bash
yeet upgrade check
```

Upgrade from verified GitHub release assets:

```bash
yeet upgrade
```

When you run it from a service workspace with `yeet.toml`, `yeet upgrade` includes every project Catch host plus the default Catch host.

Upgrade one host:

```bash
yeet upgrade --host=<catch-host>
```

Force reinstall:

```bash
yeet upgrade --force
```

Install the latest nightly release:

```bash
yeet upgrade --nightly
```

Install a specific public release:

```bash
yeet upgrade --version v0.6.1 --force
```

`--nightly` and `--version` select different targets. Use one per command; asking for two kinds of "latest" cannot end well.

## Less common but useful

Copy files:

```bash
yeet copy ./local-file <svc>:/path/in/service-data
yeet copy <svc>:/path/in/service-data ./local-file
```

See events:

```bash
yeet events <svc>
```

Stage a payload before applying it:

```bash
yeet stage --help
```

Manage service settings:

```bash
yeet service --help
yeet env --help
yeet snapshots --help
yeet host --help
```

## Requirements

Workstation:

- `yeet`
- Tailscale access to the catch host

Catch host:

- Linux with systemd
- Tailscale
- Docker, if you run container payloads
- x86_64 Linux, `/dev/kvm`, TUN/TAP, and VM filesystem tools, if you run VMs
- ZFS, only if you want ZFS-backed service roots or VM clones

## Documentation

- [Quick Start](https://yeetrun.com/docs/getting-started/quick-start)
- [Host Setup](https://yeetrun.com/docs/getting-started/host-setup)
- [Service Workspace](https://yeetrun.com/docs/getting-started/service-workspace)
- [Payloads](https://yeetrun.com/docs/payloads)
- [Networking](https://yeetrun.com/docs/concepts/networking)
- [VMs](https://yeetrun.com/docs/payloads/vms)
- [ZFS](https://yeetrun.com/docs/concepts/zfs)
- [Workflows](https://yeetrun.com/docs/operations/workflows)
- [Command reference](https://yeetrun.com/docs/cli/yeet-cli)
- [Troubleshooting](https://yeetrun.com/docs/operations/troubleshooting)
- [FAQ](https://yeetrun.com/docs/faq)

## Develop from source

Use mise so the build uses the repo-managed toolchain:

```bash
mise install
```

Build:

```bash
mise exec -- go build ./cmd/yeet
mise exec -- go build ./cmd/catch
```

Test:

```bash
mise exec -- go test ./...
```

Install hooks:

```bash
mise run install-githooks
```

Run the normal quality gate:

```bash
mise run quality
```

## Security

We built Yeet for hosts you control.

It is not a multi-tenant service platform. The default `yeet-svc` account reduces native workload privilege, but native workloads share it; Catch and the host-management helpers remain root-owned. Tailscale app permissions scope access by operation, which helps, but none of this turns a homelab into a public cloud.

Use Yeet to make private infrastructure easier to operate. Do not use it to make an unsafe boundary feel safe because the boundary now has a name.

## License

BSD 3-Clause. See `LICENSE`.
