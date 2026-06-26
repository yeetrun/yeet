<div align="center">
  <a href="https://yeetrun.com">
    <img src="https://github.com/yeetrun.png" alt="yeet logo" width="140" height="140">
  </a>
  <h1>yeet</h1>
  <p>Run services and VMs on your own Linux hosts from your workstation.</p>
  <p>
    <a href="https://yeetrun.com"><strong>yeetrun.com</strong></a>
    · <a href="https://yeetrun.com/docs/getting-started/quick-start">Quick Start</a>
    · <a href="https://yeetrun.com/docs/getting-started/installation">Install</a>
    · <a href="https://yeetrun.com/docs">Docs</a>
  </p>
</div>

Yeet is a CLI for deploying and operating services on Linux hosts you control.
You run `yeet` locally. `yeet init` installs the `catch` daemon on a host over
SSH. After setup, yeet talks to catch through Tailscale.

Use yeet for:

- Docker Compose stacks and container images.
- Local Dockerfiles and locally built images.
- Linux binaries and scripts.
- Cron jobs.
- Linux VMs on KVM-capable hosts.

Yeet fits single-operator homelabs and small private infrastructure. It expects
Linux hosts with systemd. Services currently run as root-owned systemd units on
the catch host.

## Quick Start

This path installs yeet locally, bootstraps one host, and runs a disposable
container.

### 1. Install yeet locally

Run this on your workstation:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh
```

To install the nightly build instead:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh -s -- --nightly
```

Confirm the CLI is available:

```bash
yeet --help
```

### 2. Prepare Tailscale access

Do this before running `yeet init`. Catch must join your tailnet as a tagged
device, usually `tag:catch`. User-owned catch nodes are rejected.

In the Tailscale admin console, open `Trust credentials` -> `Credential` ->
`OAuth`, then create an OAuth client secret.

Choose one setup:

- Simple setup: grant `All - Read & Write` if you are comfortable giving the
  credential broad Tailscale API access.
- Least-privilege setup: grant Auth Keys write (`auth_keys`) and select the tag
  the credential may assign. Use `tag:catch` for catch-only installs. Use an
  owner tag such as `tag:yeet` if you plan to create service Tailscale nodes
  later with `--net=ts`.

Keep the `tskey-client-...` secret ready. Interactive `yeet init` asks for it
during first setup.

See [Tailscale Setup](https://yeetrun.com/docs/concepts/tailscale#first-time-host-setup)
for the minimal policy snippet.

### 3. Bootstrap catch on a host

Run this from your workstation:

```bash
yeet init root@<machine-host>
```

`<machine-host>` is the SSH target. If you use a non-root SSH user, yeet runs
the remote install with sudo.

During first setup, paste the Tailscale OAuth client secret when prompted. For
repeatable setup, pass it explicitly:

```bash
yeet init --ts-client-secret=<secret> root@<machine-host>
```

If Docker is missing on a Debian/Ubuntu-style host, init can install it:

```bash
yeet init --install-docker --ts-client-secret=<secret> root@<machine-host>
```

For VM payloads on a host that exposes KVM and TUN/TAP, install the VM tools
too:

```bash
yeet init --install-docker --install-vm-tools --ts-client-secret=<secret> root@<machine-host>
```

Skip VM tools for the first run unless you already know the host supports VMs.
Containers, binaries, scripts, and cron jobs work without VM support.

### 4. Confirm yeet can reach catch

After `yeet init`, normal commands target the catch hostname, not the SSH
machine host.

```bash
yeet version
yeet status
```

If yeet did not save this host as the default, pass the catch hostname:

```bash
yeet --host=<catch-host> status
```

### 5. Run a disposable service

Start with a small container:

```bash
yeet run -p 18080:80 hello nginx:alpine
yeet status hello
yeet logs hello
```

Check the published port from the catch host:

```bash
yeet ssh -- curl -fsS http://127.0.0.1:18080/ >/dev/null
```

Remove the service and its data:

```bash
yeet rm --clean-data hello
```

Read the confirmation prompt before accepting. `--clean-data` deletes the
service data, including VM disks for VM services.

## Common Commands

Use the guided deploy form when you do not want to remember flags:

```bash
yeet run --web
yeet run --web <svc>
yeet run --web <svc> ./compose.yml
```

Deploy common payloads:

```bash
yeet run <svc> ./compose.yml
yeet run -p 8080:80 <svc> nginx:alpine
yeet run <svc> ./Dockerfile
yeet docker push <svc> <local-image>:<tag> --run
```

Deploy a binary, script, or cron job:

```bash
GOOS=linux GOARCH=amd64 go build -o ./bin/<svc> ./cmd/<svc>
yeet run <svc> ./bin/<svc>
yeet run <svc> ./script.sh -- --app-flag value
yeet cron <svc> ./job.sh "0 9 * * *"
```

Create a VM on a KVM-capable catch host:

```bash
yeet vm images catalog
yeet run <vm> vm://ubuntu/26.04
yeet ssh <vm>
```

After the first successful deploy, yeet writes service config to `yeet.toml`.
From that directory, redeploy the saved service with:

```bash
yeet run <svc>
```

## Operate a Service

Check status and logs:

```bash
yeet status
yeet status <svc>
yeet info <svc>
yeet logs -f <svc>
```

Open a shell or run a remote command:

```bash
yeet ssh
yeet ssh <svc>
yeet ssh -- uname -a
yeet ssh <svc> -- ls -la
```

After `yeet init`, host and regular service shells use catch over Tailscale, so
they do not require host SSH keys or a host password. VM services still connect
to the guest operating system with SSH.

Control or remove a service:

```bash
yeet restart <svc>
yeet stop <svc>
yeet start <svc>
yeet rm <svc>
```

`yeet rm <svc>` keeps service data by default. Add `--clean-data` only when you
want yeet to delete the service data too.

## Target a Host

Use `root@<machine-host>` only for `yeet init`. Use the catch hostname for
normal commands.

```bash
CATCH_HOST=<catch-host> yeet status
yeet --host=<catch-host> status
yeet status@<catch-host>
yeet run <svc>@<catch-host> ./compose.yml
```

Save a default catch host:

```bash
yeet prefs --host=<catch-host> --save
```

## Upgrade

Check the local CLI and the selected catch host:

```bash
yeet upgrade check
```

Upgrade both from verified GitHub release assets:

```bash
yeet upgrade
```

For projects with multiple catch hosts in `yeet.toml`, check and upgrade all of
them explicitly:

```bash
yeet upgrade check --all
yeet upgrade --all
```

To reinstall a release even when a component already looks current, newer, or
locally built:

```bash
yeet upgrade --all --force
```

To install a specific public release:

```bash
yeet upgrade --all --version v0.6.1 --force
```

## Optional Capabilities

Start with the quick path before adding optional features.

- Docker is required for container payloads.
- VMs require x86_64 Linux, KVM at `/dev/kvm`, TUN/TAP, and VM filesystem
  tools on the catch host.
- `--net=svc` creates a private service network, adds yeet DNS, and sends
  ordinary outbound internet through the catch host.
- `--net=svc,ts` keeps `svc` behavior and gives the service its own Tailscale
  identity. Use this for most Tailscale-exposed services.
- `--net=lan` requests a LAN or VLAN address. Outbound internet comes from the
  DHCP gateway on that network.
- Plain `--net=ts` is tailnet-only unless you configure a Tailscale exit node.
- ZFS is optional and enables dataset-backed service roots, snapshots, and fast
  repeated VM disk clones.

Use the manual before enabling optional storage or networking:

- [Payloads](https://yeetrun.com/docs/payloads)
- [Networking](https://yeetrun.com/docs/concepts/networking)
- [VMs](https://yeetrun.com/docs/payloads/vms)
- [ZFS](https://yeetrun.com/docs/concepts/zfs)

## Documentation

- [Quick Start](https://yeetrun.com/docs/getting-started/quick-start)
- [Installation](https://yeetrun.com/docs/getting-started/installation)
- [Workflows](https://yeetrun.com/docs/operations/workflows)
- [Payloads](https://yeetrun.com/docs/payloads)
- [yeet command reference](https://yeetrun.com/docs/cli/yeet-cli)
- [catch reference](https://yeetrun.com/docs/cli/catch-cli)
- [Troubleshooting](https://yeetrun.com/docs/operations/troubleshooting)
- [FAQ](https://yeetrun.com/docs/faq)

## Develop from Source

Use mise to install the repo toolchain:

```bash
mise install
```

Build and test:

```bash
mise exec -- go build ./cmd/yeet
mise exec -- go build ./cmd/catch
mise exec -- go test ./...
```

Install local hooks before contributor work:

```bash
mise run install-githooks
```

Run the normal quality gate before publishing changes:

```bash
mise run quality
```

## Security

Yeet is for hosts you control. It is not a multi-tenant platform. Services
managed by catch currently run as root-owned systemd units.

## License

BSD 3-Clause. See `LICENSE`.
