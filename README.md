<div align="center">
  <a href="https://yeetrun.com">
    <img src="https://github.com/yeetrun.png" alt="yeet logo" width="140" height="140">
  </a>
  <h1>yeet</h1>
  <p>Homelab service manager built around Tailscale RPC.</p>
  <p>
    <a href="https://yeetrun.com"><strong>yeetrun.com</strong></a>
    · <a href="https://yeetrun.com/docs/getting-started/quick-start">Quick Start</a>
    · <a href="https://yeetrun.com/docs/getting-started/installation">Install</a>
    · <a href="https://yeetrun.com/docs/getting-started/first-run-validation">First-Run Validation</a>
    · <a href="https://yeetrun.com/docs">Docs</a>
  </p>
</div>

Yeet is open source homelab infrastructure tooling. You run the `yeet` CLI from
your workstation and install the `catch` daemon on Linux hosts you control.
From there, yeet deploys containers, host services, cron jobs, and
Firecracker-backed Ubuntu VMs over Tailscale/tsnet RPC.

Yeet is intentionally opinionated:

- Hosts run Linux with systemd.
- SSH is used for `yeet init`; RPC uses catch's embedded tsnet node.
- Docker is used for container payloads.
- Services are currently managed as root-owned systemd units.
- VM payloads require x86_64 Linux, KVM (`/dev/kvm`), TUN/TAP, and VM
  filesystem/networking tools on the catch host.

Within those constraints, the release path is intended to be installable on a
fresh Ubuntu/Debian-style host with SSH access.

## Install

Install the release binary:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh
```

Nightly build:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh -s -- --nightly
```

## Bootstrap a Host

Start with a Linux host that has systemd and SSH access. Docker can be
installed by `yeet init` on Debian/Ubuntu-style hosts:

```bash
yeet init --install-docker root@<machine-host>
```

If catch needs first-time Tailscale enrollment, `yeet init` prints a login URL.
For unattended bootstrap, pass a Tailscale auth key for the catch node:

```bash
yeet init --install-docker --ts-auth-key=<key> root@<machine-host>
```

Host names matter:

- `root@<machine-host>` is the SSH target used only for init/install.
- `CATCH_HOST`, `--host`, and `<svc>@<host>` refer to the catch tsnet hostname.

See [Installation](https://yeetrun.com/docs/getting-started/installation) and
[Tailscale](https://yeetrun.com/docs/concepts/tailscale) for details.

## Validate the First Run

Confirm the control plane is reachable:

```bash
yeet version
yeet status
```

If you have Tailscale installed locally, `yeet list-hosts` can also discover
tagged catch nodes. It is optional for normal RPC because yeet embeds tsnet.

Then run a disposable container:

```bash
yeet run -p 18080:80 yeet-smoke-web nginx:alpine
yeet ssh -- curl -fsS http://127.0.0.1:18080/ >/dev/null
yeet rm --clean-data yeet-smoke-web
```

For the full fresh-host playbook, including script services, cron timers, VM
capability, LAN networking, and ZFS checks, use
[First-Run Validation](https://yeetrun.com/docs/getting-started/first-run-validation).

## Common Workflows

Docker Compose:

```bash
yeet run <svc> ./compose.yml
yeet logs -f <svc>
yeet run --pull <svc> ./compose.yml
```

Docker image:

```bash
yeet run -p 8080:80 <svc> nginx:alpine
```

Dockerfile:

```bash
yeet run <svc> ./Dockerfile
```

Binary or script:

```bash
GOOS=linux GOARCH=amd64 go build -o ./bin/<svc> ./cmd/<svc>
yeet run <svc> ./bin/<svc>
yeet run <svc> ./script.sh -- --app-flag value
```

Cron job:

```bash
yeet cron <svc> ./job.sh "0 9 * * *"
```

Ubuntu VM on a KVM-capable host:

```bash
yeet run devbox vm://ubuntu/26.04
yeet ssh devbox
yeet vm console devbox
```

Local image built on your workstation:

```bash
yeet docker push <svc> <local-image>:<tag> --run
```

After the first successful deploy, yeet writes a `yeet.toml` replay file. You
can usually rerun the same service with:

```bash
yeet run <svc>
```

See [Workflows](https://yeetrun.com/docs/operations/workflows) and
[Payloads](https://yeetrun.com/docs/payloads) for the complete guides.

## Optional Host Capabilities

Yeet works without every optional feature. The host determines which payloads
and network modes are available:

- Docker is required for container payloads.
- x86_64 Linux, KVM, TUN/TAP, and VM filesystem/networking tools are required
  for VM payloads. `yeet init` checks this and can offer to install missing
  Debian/Ubuntu packages when the host can run VMs.
- LAN/macvlan networking requires a host network where macvlan and DHCP make
  sense.
- ZFS is optional and enables dataset-backed service roots, snapshots, and fast
  repeated VM disk clones.
- `--net=ts` service networking requires Tailscale auth for each service netns.

Yeet warns during init or deploy when a host cannot support a requested
feature. See [Networking](https://yeetrun.com/docs/concepts/networking),
[VMs](https://yeetrun.com/docs/payloads/vms), and
[ZFS](https://yeetrun.com/docs/concepts/zfs).

## Documentation

The docs site is the user manual and the source of truth for behavior:

- [Quick Start](https://yeetrun.com/docs/getting-started/quick-start)
- [Installation](https://yeetrun.com/docs/getting-started/installation)
- [First-Run Validation](https://yeetrun.com/docs/getting-started/first-run-validation)
- [Workflows](https://yeetrun.com/docs/operations/workflows)
- [Payloads](https://yeetrun.com/docs/payloads)
- [Architecture](https://yeetrun.com/docs/concepts/architecture)
- [Networking](https://yeetrun.com/docs/concepts/networking)
- [Tailscale](https://yeetrun.com/docs/concepts/tailscale)
- [ZFS](https://yeetrun.com/docs/concepts/zfs)
- [CLI Reference](https://yeetrun.com/docs/cli/yeet-cli)
- [Troubleshooting](https://yeetrun.com/docs/operations/troubleshooting)
- [FAQ](https://yeetrun.com/docs/faq)

## Development from Source

Use mise to install the pinned toolchain from `.mise.toml`:

```bash
curl https://mise.run | sh
echo 'eval "$(mise activate zsh)"' >> ~/.zshrc
mise install
```

Build locally:

```bash
go build ./cmd/yeet
go build ./cmd/catch
```

Install repo hooks once:

```bash
mise run install-githooks
```

Run the normal local quality gate:

```bash
mise run quality
```

Heavier checks are available for release or deeper quality work:

```bash
mise run race
mise run fuzz
mise run mutation
mise run quality:goal
```

## Security Notes

Services managed by `catch` currently run as root. That is acceptable for a
single-operator homelab, but it is not a good default for production or
multi-tenant setups. See the [FAQ](https://yeetrun.com/docs/faq) for current
limitations.

## License

BSD 3-Clause. See `LICENSE`.
