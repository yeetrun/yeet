# yeet

Deploy containers, VMs, binaries, scripts, and cron jobs from your workstation to Linux hosts.

The normal way to deploy small infrastructure is to accidentally build a platform. You start with SSH, then add shell scripts, then add a deploy box, then add a secrets story, then add a dashboard, then discover that your dashboard is mostly a slower way to run SSH.

`yeet` tries not to do that.

You run `yeet` locally. It installs a small daemon called `catch` on a Linux host. After that, commands go over Tailscale to the host, and catch turns them into boring Linux things: systemd units, Docker Compose projects, containers, cron jobs, files, and VMs.

Not magic. Just fewer places for state to hide.

<p>
  <a href="https://yeetrun.com"><strong>yeetrun.com</strong></a>
  · <a href="https://yeetrun.com/docs/getting-started/quick-start">Quick Start</a>
  · <a href="https://yeetrun.com/docs/getting-started/host-setup">Host Setup</a>
  · <a href="https://yeetrun.com/docs">Docs</a>
</p>

## What yeet is for

Use yeet when you have one or more Linux hosts and you want to run real services without turning your homelab into a miniature cloud provider.

Yeet can deploy:

- Docker Compose stacks
- Container images
- Local Dockerfiles
- Linux binaries
- Shell scripts
- Cron jobs
- Linux VMs on KVM-capable hosts

It fits single-operator homelabs and small private infrastructure. It expects
Linux hosts with systemd. New native binaries, scripts, and cron jobs run as
the unprivileged `yeet-svc` account by default; Docker and VM identities stay
in their own runtimes.

Yeet is for hosts you control. It is not a multi-tenant platform.

## The model

There are two moving parts:

- `yeet`: the CLI on your workstation.
- `catch`: the daemon on each Linux host you manage.

First setup uses SSH:

```bash
yeet init root@<machine-host>
```

After setup, normal commands target the catch host over Tailscale:

```bash
yeet status
yeet run <svc> ./compose.yml
yeet logs -f <svc>
```

Use the machine hostname for `yeet init`. Use the catch hostname for normal yeet commands after setup. The default catch hostname is `catch`.

## Quick start

This gets you from nothing to a disposable container.

### 1. Install yeet locally

```bash
curl -fsSL https://yeetrun.com/install.sh | sh
```

Nightly build:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh -s -- --nightly
```

Check it:

```bash
yeet --help
```

### 2. Prepare Tailscale

Catch joins your tailnet as a tagged device, usually `tag:catch`. User-owned catch nodes are rejected.

You need a Tailscale OAuth client secret. In the Tailscale admin console, go to:

```text
Trust credentials -> Credential -> OAuth
```

For the first install, the simple path is broad access:

```text
All - Read & Write
```

The tighter path is Auth Keys write access for the tag catch will use, usually `tag:catch`.

Your tailnet policy also needs to allow the setup user to reach catch on TCP `41548` with the `yeetrun.com/app/yeet` app permissions:

- `read`
- `manage`
- `ssh`

First setup needs all three. Later, split them if you want narrower roles.

### 3. Install catch on a host

```bash
yeet init root@<machine-host>
```

If you SSH as a non-root user, yeet runs the remote install with sudo.

Interactive setup asks for the Tailscale OAuth client secret. Catch stores its
state in this directory by default:

```text
/var/lib/yeet
```

The service root defaults to `<data-dir>/services`, which is
`/var/lib/yeet/services` with the default data directory. Set `--data-dir` or
`--services-root` during init when the host needs a different filesystem path.
Explicit custom roots are preserved during upgrades and guided migrations.

If Docker is missing on a Debian/Ubuntu-style host, interactive setup asks
before installing it. If the host can run VMs, setup can ask about VM tools too.

If the host has ZFS and you want service data on datasets:

```bash
yeet init --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services root@<machine-host>
```

Rerunning `yeet init` upgrades catch without changing explicit custom or ZFS
roots.
When an interactive upgrade finds the exact legacy home-directory layout, it
can offer to move that state to `/var/lib/yeet`. If init cannot prompt, run the
same migration explicitly:

```bash
yeet host set \
  --data-dir=/var/lib/yeet \
  --services-root=/var/lib/yeet/services \
  --migrate-services=all \
  --yes
yeet host cleanup --from=/root/yeet-data --yes
```

`yeet host set` moves and validates the active state but does not delete the old
tree. Run cleanup separately. Cleanup refuses arbitrary paths, revalidates the
active Catch and service state, and removes only the journaled inactive source.
If deletion alone fails, rerun the same cleanup command to resume it safely.

ZFS datasets are not copied or deleted implicitly. Dataset-backed data and
nested datasets stay in place unless you manage them explicitly.

### 4. Confirm the host works

```bash
yeet version
yeet status
```

If you have more than one catch host:

```bash
yeet --host=<catch-host> status
```

Save a default:

```bash
yeet config --host=<catch-host>
```

### 5. Create a service workspace

Yeet writes `yeet.toml` after a successful deploy. Put services in a directory you mean to keep.

```bash
mkdir -p ~/yeet-services
cd ~/yeet-services
```

After setup, yeet can remember this workspace in `$XDG_CONFIG_HOME/yeet/config.toml`, so commands from other directories can still find the right `yeet.toml`.
If you already have a `yeet.toml` in the current directory, interactive commands
such as `yeet status` can offer to adopt that directory as a saved workspace.

This file is the local state that makes commands from other directories behave
like they were run from the workspace.

### 6. Run something disposable

```bash
yeet run -p 18080:80 hello nginx:alpine
yeet status hello
yeet logs hello
```

Check the published port from the catch host:

```bash
yeet ssh -- curl -fsS http://127.0.0.1:18080/ >/dev/null
```

Remove it:

```bash
yeet rm --clean hello
```

Read the prompt. `--clean` deletes service data, including VM disks for VM services, and removes the local `yeet.toml` entry.

## Common deploys

Run these from a service workspace.

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

### Cron job

```bash
yeet cron <svc> ./job.sh "0 9 * * *"
```

New native binaries, scripts, and cron-style timers run as the managed
`yeet-svc` system account by default. Choose an existing host account with
`--run-as=USER[:GROUP]` when the workload needs it:

```bash
yeet run <svc> ./bin/<svc> --run-as=app:app
yeet cron <svc> ./job.sh "0 9 * * *" --run-as=backup
```

Docker execution identities stay in Compose (`user:`), and VM host execution
uses the separate `yeet-vm` jailer account. Existing native services keep their
current identity until you migrate one explicitly:

```bash
yeet service set <svc> --run-as=yeet-svc
yeet service set <svc> \
  --service-root=/var/lib/yeet/services/<svc> \
  --copy \
  --run-as=yeet-svc
```

The migration stops the native workload, verifies the service root, updates
ownership and systemd definitions as one rollback-safe transaction, and then
restores its prior running state. ZFS-backed roots remain on their configured
dataset. Non-root native workloads cannot request privileged host ports below
1024; use a higher host port or keep that workload explicitly root-owned.

Custom service roots must live below host-controlled directories. Every parent
must be owned by root and must not be group- or world-writable; `/srv/apps` and
ZFS mountpoints are typical choices, while a workload-owned home directory is
rejected because the workload could replace paths while Catch operates on them.
For an operator-created account, systemd also applies that account's configured
supplementary groups. Review memberships such as `docker` before selecting it.
`yeet ssh <svc>` deliberately clears supplementary groups for a more restricted
service shell.

### VM

```bash
yeet vm images catalog
yeet run <vm> vm://ubuntu/26.04
yeet ssh <vm>
```

Yeet launches Firecracker through the matching Firecracker jailer. Catch
prepares the VM's host resources as root, and the jailer runs the VMM as the
static, non-login `yeet-vm` host account. This host account is separate from
the VM guest login user and from native-service `--run-as` identities.

Yeet automatically creates `yeet-vm` on the first VM preparation or during an
upgrade that finds VMs. Custom data roots, custom service roots, and ZFS-backed
VM storage remain supported because Yeet derives their paths from stored
configuration.

The host Firecracker and jailer pair has its own lifecycle. It is separate from
the guest root filesystem, guest packages, guest kernel, and guest login user.
See what each VM has running, configured, staged, and available for rollback:

```bash
yeet vm runtime status
yeet vm runtime status <vm> --format=json-pretty
```

Runtime policy is manual by default. `yeet vm runtime update` refreshes the
host runtime cache but does not stage or restart a VM. `upgrade` stages an exact
runtime for the next start; add `--restart` only when downtime is acceptable:

```bash
yeet vm runtime update
yeet vm runtime upgrade <vm>
yeet vm runtime upgrade <vm> --restart
yeet vm runtime rollback <vm> --restart
```

A guest package upgrade cannot request a host runtime change. A normal guest
reboot can consume a runtime that an operator or host policy already staged,
but it cannot select or download one. Catch upgrades also leave running VMs
alone. The optional `stage-on-restart` policy stages promoted releases without
restarting VMs.

Create and restore a VM disk recovery point on a ZFS-backed VM:

```bash
yeet snapshots create <vm> --comment "before package upgrade"
yeet snapshots restore <vm> <snapshot> --stop --start --yes
```

For a running VM, catch pauses the guest while it takes one atomic ZFS snapshot
of the disk, then resumes it. The snapshot is crash-consistent disk state, not
guest memory or VMM runtime state. Raw-disk VMs cannot be snapshotted. Restore
replaces the VM disk state only.

Service names created by `yeet run` must use lowercase letters, numbers, and dashes, start with a letter, and end with a letter or number.

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

After `yeet init`, host and regular service shells use catch over Tailscale. They do not need your original host SSH key or host password. VM services still connect to the guest operating system with SSH keys.

Lifecycle:

```bash
yeet restart <svc>
yeet stop <svc>
yeet start <svc>
yeet rm <svc>
```

`yeet rm <svc>` keeps service data by default and prompts before removing the local config entry. Add `--clean` only when you want the data gone too.

## Targeting hosts

Use `root@<machine-host>` for `yeet init`.

Use catch hostnames for normal commands:

```bash
CATCH_HOST=<catch-host> yeet status
yeet --host=<catch-host> status
yeet status@<catch-host>
yeet run <svc>@<catch-host> ./compose.yml
```

For a second catch host, choose a distinct catch hostname during setup:

```bash
yeet --host=morpheus-catch init root@<machine-host>
```

Save the default:

```bash
yeet config --host=<catch-host>
```

## Networking

Yeet has a few network modes because services have different reachability and routing needs. Choose the mode that matches how the service should be reached.

- `--net=svc`: private service network, yeet DNS, normal outbound internet through the catch host.
- `--net=svc,ts`: `svc` behavior plus a service-owned Tailscale identity. Use this for most Tailscale-exposed services.
- `--net=lan`: LAN or VLAN address. Outbound internet comes from that network's DHCP gateway.
- `--net=ts`: tailnet-only unless you configure a Tailscale exit node.
- `--net=iso`: stable private address with public IPv4 egress, public-only DNS,
  and no workload-initiated access to catch, LAN, `svc`, Tailscale, or other
  isolated projects. Catch can still connect to the workload on any port.
- `--net=iso,ts`: `iso` behavior plus a service-owned Tailscale identity for
  supported container-backed payloads.

VM `--net=lan` attaches the guest TAP to a host bridge. On supported Debian/Ubuntu hosts, yeet can prepare `br0` during `yeet init` or before the first VM LAN create.

ISO supports VMs and container-backed payloads. VMs use `iso` alone and can
install Tailscale inside the guest when needed. Native binaries, scripts, and
cron jobs cannot use ISO while they run as root-owned host services. ISO also
rejects published ports and unsafe Compose features instead of pretending they
are contained.

Read the docs before combining networking modes with real services. Future you is the person who has to debug it.

## Storage

ZFS is optional.

If you use a ZFS services root, yeet treats it as a dataset prefix. Services under it use child datasets, which gives you snapshots and fast VM disk clones.

That is persistent storage, so read the ZFS docs first if the data matters.

## Upgrades

Check local yeet and catch hosts:

```bash
yeet upgrade check
```

Upgrade from verified GitHub release assets:

```bash
yeet upgrade
```

When run from a service workspace with `yeet.toml`, `yeet upgrade` includes all project catch hosts plus the default catch host.

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

`--nightly` and `--version` select different targets, so use one of them per command.

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

Use mise:

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

Yeet is for hosts you control.

It is not a multi-tenant service platform. The default `yeet-svc` account
reduces native workload privilege but is shared across those workloads, while
Catch and host-management helpers remain root-owned. Access is operation-scoped
through Tailscale app permissions, and that helps, but it does not turn your
homelab into a public cloud.

This is a tool for making private infrastructure easier to operate, not for making unsafe boundaries safe by naming them.

## License

BSD 3-Clause. See `LICENSE`.
