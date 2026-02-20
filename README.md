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

3) From the repo root, install tools (Go 1.25.5) + bootstrap a host:

```bash
mise install
mise run init-host -- root@<host>
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

Note: `yeet run` for compose does not pull new images by default. To refresh images, use `yeet run --pull <svc> ./compose.yml` or `yeet docker update <svc>`.
If you need to redeploy even when nothing changed, use `yeet run --force <svc> ./compose.yml`.
With a stored `yeet.toml` payload, `yeet run <svc> --force` also works.
Note: Docker hosts must enable the containerd snapshotter so pushed images show up locally (see Installation in the docs).

Other common variants (in order of use):

```bash
yeet run <svc> ./Dockerfile
yeet run <svc> ./bin/<svc> -- --app-flag value
```

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
