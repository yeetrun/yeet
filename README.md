<div align="center">
  <a href="https://yeetrun.com">
    <img src="https://github.com/yeetrun.png" alt="yeet logo" width="140" height="140">
  </a>
  <h1>yeet</h1>
  <p>Personal homelab service manager built around Tailscale RPC.</p>
  <p>
    <a href="https://yeetrun.com"><strong>yeetrun.com</strong></a>
    · <a href="https://yeetrun.com/docs/getting-started/quick-start">Quick Start</a>
    · <a href="https://yeetrun.com/docs/getting-started/installation">Install</a>
    · <a href="https://yeetrun.com/docs">Docs</a>
  </p>
</div>

If you landed here first, start with the docs and installation guide on [yeetrun.com](https://yeetrun.com).

See the [Architecture](https://yeetrun.com/docs/concepts/architecture) page for how the pieces fit together.

## Read This First

This repository is **personal infrastructure tooling** for how I run my homelab. It is not intended for a general audience, likely will not work for you as-is, and may rely on assumptions, configs, and workflows that only exist in my environment. Use it only as a reference or starting point.

## Install yeet (release binary)

```bash
curl -fsSL https://yeetrun.com/install.sh | sh
```

Nightly build:

```bash
curl -fsSL https://yeetrun.com/install.sh | sh -s -- --nightly
```

## Toolchain Setup (Recommended: mise)

If you already have Go in your `PATH`, you can skip mise and use the Go commands elsewhere in this README. If not, the quickest path is to use mise to install the toolchain and run the bootstrap task.

1) Install mise (use a package manager like Homebrew/apt/dnf/pacman, or run the installer script):

```bash
curl https://mise.run | sh
```

2) Activate mise in your shell (zsh example — swap for bash/fish as needed):

```bash
echo 'eval "$(mise activate zsh)"' >> ~/.zshrc
```

3) From the repo root, install tools (Go 1.26.3) + bootstrap a host:

```bash
mise install
mise run init-host -- root@<host>
```

## Development Quality Gates

Install the repo hooks once:

```bash
mise install
mise run install-githooks
```

Run the same deterministic baseline checks manually:

```bash
mise run quality
```

Codex project hooks live under `.codex/`. They are lightweight agent-loop
guardrails for this repo, not replacements for pre-commit. The Stop hook checks
final answers that claim clean, pushed, tagged, or released state against git
and the website submodule release checklist.

Agent navigation docs live in `docs/agent/`. Start with
`docs/agent/codebase-map.md` when orienting a Codex session to the repository.
Task-specific workflows live in `.codex/skills/`.

The quality gate scans for private local references, runs `go test` with
coverage, checks CRAP hotspots, runs `golangci-lint` with complexity and
bug-risk linters, and writes a churn/coverage hotspot report to
`.tmp/quality/hotspots.txt`. Existing findings are tracked in
`tools/quality/baseline/`; new findings fail the hook. When a hotspot is fixed,
refresh the baseline intentionally:

```bash
mise run quality:baseline
```

Heavier empirical checks are available outside the normal pre-commit path:

```bash
mise run race
mise run fuzz
mise run mutation
mise run hotspots
```

The long-term quality destination is tracked separately as a heavy
industry-standard goal: at least 80% total coverage, zero CRAP hotspots, zero
golangci findings, 80% mutation score on the bounded mutation target set, the
race detector passing, and at least four active fuzz targets. Check progress
with:

```bash
mise run quality:goal
```

## High-Level Overview

yeet is a lightweight client + server setup for deploying and managing services on remote Linux machines. The primary use case is running Docker images on a host over Tailscale with a tiny workflow (`yeet run <svc> <image>`).

- Run Docker images or Compose stacks on a remote host
- Push locally-built images into an internal registry when you need them
- Manage service lifecycle (start/stop/restart/logs/status)
- Push updates over Tailscale RPC
- Support a few networking modes used in my lab (e.g., Tailscale, macvlan)

## Docker Quickstart (Most Common Path: Compose)

Host terminology: `yeet init root@<host>` uses the SSH **machine host**. `yeet run <svc>@<host>` (and `CATCH_HOST`) uses the **catch host** (Tailscale/tsnet hostname).

```bash
yeet init root@<host>
yeet run <svc> ./compose.yml
yeet ssh
```

Note: from a repo checkout, `yeet init` builds and uploads `catch`. Released yeet binaries (or `--from-github`) download the latest stable release; add `--nightly` for nightly builds.

If your compose uses an env file, upload it before deploy:

```bash
yeet run --env-file=prod.env <svc> ./compose.yml
```

Note: `yeet run` for compose does not pull new images by default. To check for
available upstream image updates without changing containers, use
`yeet docker outdated`; the default table stays compact, and JSON formats
include full image digests. To refresh images, use
`yeet run --pull <svc> ./compose.yml`, `yeet docker update <svc...>`, or
`yeet docker update --outdated` to update every compose service with available
image updates. Explicit updates may mix hosts with `yeet docker update foo
bar@catch-b baz`; unqualified services still use `yeet.toml` or the default
catch host. Batch updates print a short host/service marker, then stream the
same output as `yeet docker update <svc>`. If `--outdated` cannot classify a
reported service because the scan returns unknown or error rows, it prints those
skipped rows and exits nonzero after running any updateable services.
If you need to redeploy even when nothing changed, use `yeet run --force <svc> ./compose.yml`.
With a stored `yeet.toml` payload, `yeet run <svc> --force` also works.
Note: Docker hosts must enable the containerd snapshotter so pushed images show up locally (see Installation in the docs).

Other common variants (in order of use):

```bash
yeet run <svc> ./Dockerfile
yeet run <svc> ./bin/<svc> -- --app-flag value
```

Custom service root on the catch host:

```bash
yeet run vaultwarden ./compose.yml --service-root=/srv/apps/vaultwarden
yeet run vaultwarden ./compose.yml --service-root=tank/apps/vaultwarden --zfs
```

Without `--zfs`, `--service-root` must be an absolute filesystem path on the
catch host. With `--zfs`, `--service-root` is a ZFS dataset name such as
`tank/apps/vaultwarden`; catch accepts an existing dataset or runs
`zfs create tank/apps/vaultwarden`, then uses the dataset mountpoint as the
service root. Parent datasets must already exist.

For filesystem paths, the parent directory (`/srv/apps` in this example) must
already exist; yeet can create the final service directory.

ZFS-backed services get yeet-managed snapshots before risky changes. By
default, catch snapshots before a redeploy, a Docker image update, or a
ZFS-backed service-root migration; first deploys are skipped because there is
nothing useful to recover. Snapshot creation is required by default, so the
change aborts if `zfs snapshot` fails.

The server-wide default is enabled, keeps the newest 5 yeet-created snapshots,
and prunes yeet-created snapshots older than 7 days:

```bash
yeet snapshots defaults show
yeet snapshots defaults set --enabled=false
yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d
```

Override the snapshot policy for one service with `yeet service set`:

```bash
yeet service set vaultwarden --snapshots=off
yeet service set vaultwarden --snapshots=on --snapshot-keep-last=3 --snapshot-max-age=72h
yeet service set vaultwarden --snapshot-required=false
yeet service set vaultwarden --snapshot-events=run,docker-update
yeet service set vaultwarden --snapshots=inherit
```

The root contains `bin`, `run`, `env`, and `data`. `yeet run` can choose the
initial root for a new service, but it cannot move an existing service. To move
a stopped service root, use:

```bash
yeet service set vaultwarden --service-root=/mnt/fast/vaultwarden --copy
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --copy
yeet service set vaultwarden --service-root=/mnt/fast/vaultwarden --empty
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --empty
```

`yeet service set` leaves the old root in place. Non-interactive migrations
must choose `--copy` to copy existing files or `--empty` to start with an empty
root. For the migration examples above, `/mnt/fast` must already exist.

If you moved a service from outside the project directory, sync the live root
identity back into the local TOML replay recipe:

```bash
yeet service sync vaultwarden
yeet service sync vaultwarden --config ~/yeet-services/yeet.toml
yeet service sync --all --config ~/yeet-services/yeet.toml
```

The catch DB remains the source of truth for the live service. `yeet service
sync` updates only existing entries in `yeet.toml`; it does not import
arbitrary catch services because catch does not know the local payload or env
file paths. For ZFS-backed roots, the local config stores the dataset name with
`service_root_zfs = true`. If a service has snapshot overrides, sync also stores
the TOML replay fields such as `snapshots`, `snapshot_keep_last`, and
`snapshot_max_age`.

Less common (registry image or pushing a local image):

```bash
yeet run <svc> nginx:latest
yeet docker push <svc> <local-image>:<tag> --run
```

## Tailscale OAuth Setup

If you use `--net=ts` for service networking, configure an OAuth client secret
on the catch host:

```bash
yeet tailscale --setup
# or
yeet tailscale --setup --client-secret=tskey-client-...
```

The interactive flow links you to the admin console steps for creating a tag
and trust credential, then writes the secret to the catch host for you.

## Documentation

The docs site is the user manual and the source of truth for behavior and workflows:

- [Quick Start](https://yeetrun.com/docs/getting-started/quick-start)
- [Workflows](https://yeetrun.com/docs/operations/workflows) (Docker-first walkthroughs)
- [Installation](https://yeetrun.com/docs/getting-started/installation)
- [Architecture](https://yeetrun.com/docs/concepts/architecture)
- [CLI Overview](https://yeetrun.com/docs/cli/cli-overview)
- [yeet CLI](https://yeetrun.com/docs/cli/yeet-cli)
- [catch CLI](https://yeetrun.com/docs/cli/catch-cli)
- [Networking](https://yeetrun.com/docs/concepts/networking)
- [Tailscale](https://yeetrun.com/docs/concepts/tailscale)
- [Service Types](https://yeetrun.com/docs/concepts/service-types)
- [Configuration & Prefs](https://yeetrun.com/docs/concepts/configuration-and-prefs)
- [Data Layout](https://yeetrun.com/docs/concepts/data-layout)
- [Troubleshooting](https://yeetrun.com/docs/operations/troubleshooting)
- [Development](https://yeetrun.com/docs/development)
- [FAQ](https://yeetrun.com/docs/faq)

## Components

- **yeet**: client CLI used from my workstation (see the [yeet CLI](https://yeetrun.com/docs/cli/yeet-cli) reference)
- **catch**: service manager daemon running on homelab hosts (see the [catch CLI](https://yeetrun.com/docs/cli/catch-cli) reference)

## How I Run It

In my homelab, I run `catch` on each host and use `yeet` to push binaries/images, manage versions, and poke at service state over Tailscale. The [Networking](https://yeetrun.com/docs/concepts/networking) and [Configuration & Prefs](https://yeetrun.com/docs/concepts/configuration-and-prefs) pages describe the host targeting and network modes that make this work in my lab. The workflow is optimized for my machines and my network topology, not for general compatibility.

## Security Notes

Currently, services managed by `catch` run as root. This is fine for my lab, but it is not a good default for production or multi-tenant setups. See the [FAQ](https://yeetrun.com/docs/faq) for current limitations.

## License

BSD 3-Clause. See `LICENSE`.
