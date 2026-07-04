# yeet

Run services and VMs on Linux hosts you control, from the workstation you already use.

The normal way to deploy small infrastructure is to accidentally build a platform. You start with SSH, then add shell scripts, then add a deploy box, then add a secrets story, then add a dashboard, then discover that your dashboard is mostly a slower way to run SSH.

`yeet` tries not to do that.

You run `yeet` locally. It installs a small daemon called `catch` on a Linux host. After that, commands go over Tailscale to the host, and catch turns them into boring Linux things: systemd units, Docker Compose projects, containers, cron jobs, files, and VMs.

Not magic. Just fewer places for state to hide.

<p>
  <a href="https://yeetrun.com"><strong>yeetrun.com</strong></a>
  · <a href="https://yeetrun.com/docs/getting-started/quick-start">Quick Start</a>
  · <a href="https://yeetrun.com/docs/getting-started/installation">Install</a>
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

It fits single-operator homelabs and small private infrastructure. It expects Linux hosts with systemd. Services currently run as root-owned systemd units on the catch host.

That last sentence is important. Yeet is for hosts you control. It is not a multi-tenant platform, and pretending otherwise would be how we get a very exciting security incident.

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

The machine hostname is for bootstrapping. The catch hostname is for operating. This distinction sounds fussy until the first time DNS, SSH keys, and Tailscale names all disagree with each other. Then it becomes the only sentence you care about.

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

First setup needs all three. Later, split them if you want narrower roles. This is not bureaucracy; this is where the control plane lives.

### 3. Install catch on a host

```bash
yeet init root@<machine-host>
```

If you SSH as a non-root user, yeet runs the remote install with sudo.

Interactive setup asks for the Tailscale OAuth client secret and a data directory. The default is:

```text
$HOME/yeet-data
```

If Docker is missing on a Debian/Ubuntu-style host and you want container payloads:

```bash
yeet init --install-docker root@<machine-host>
```

If the host can run VMs too:

```bash
yeet init --install-docker --install-vm-tools root@<machine-host>
```

If the host has ZFS and you want service data on datasets:

```bash
yeet init --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services root@<machine-host>
```

Rerunning `yeet init` keeps the existing storage layout and upgrades catch.

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
yeet prefs --host=<catch-host> --save
```

### 5. Create a service workspace

Yeet writes `yeet.toml` after a successful deploy. Put services in a directory you mean to keep.

```bash
mkdir -p ~/yeet-services
cd ~/yeet-services
```

This file is the boring local state. Boring local state is good. Hidden state is how tools become haunted.

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

### VM

```bash
yeet vm images catalog
yeet run <vm> vm://ubuntu/26.04
yeet ssh <vm>
```

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

After `yeet init`, host and regular service shells use catch over Tailscale. They do not need your original host SSH key or host password. VM services still connect to the guest operating system with SSH, because a VM is an actual machine and actual machines continue to be annoying.

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

Save the default:

```bash
yeet prefs --host=<catch-host> --save
```

## Networking

Yeet has a few network modes because services have a few different shapes. There is no single correct answer. There is only the answer that fails least badly for the service you are running.

- `--net=svc`: private service network, yeet DNS, normal outbound internet through the catch host.
- `--net=svc,ts`: `svc` behavior plus a service-owned Tailscale identity. Use this for most Tailscale-exposed services.
- `--net=lan`: LAN or VLAN address. Outbound internet comes from that network's DHCP gateway.
- `--net=ts`: tailnet-only unless you configure a Tailscale exit node.

VM `--net=lan` attaches the guest TAP to a host bridge. On supported Debian/Ubuntu hosts, yeet can prepare `br0` during `yeet init` or before the first VM LAN create.

Read the docs before combining networking modes with real services. Future you is the person who has to debug it.

## Storage

ZFS is optional.

If you use a ZFS services root, yeet treats it as a dataset prefix. Services under it use child datasets, which gives you snapshots and fast VM disk clones.

That is useful. It is also storage. Storage is where optimistic assumptions go to become incident reports. Read the ZFS docs first if the data matters.

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

Install a specific public release:

```bash
yeet upgrade --version v0.6.1 --force
```

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
- [Installation](https://yeetrun.com/docs/getting-started/installation)
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

It is not a multi-tenant service platform. Services managed by catch currently run as root-owned systemd units. Access is operation-scoped through Tailscale app permissions, and that helps, but it does not turn your homelab into a public cloud.

This is a tool for making private infrastructure easier to operate, not for making unsafe boundaries safe by naming them.

## License

BSD 3-Clause. See `LICENSE`.
