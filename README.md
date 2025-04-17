# yeet

A personal homelab service manager built around Tailscale RPC. See the [Architecture](https://github.com/shayne/yeet/wiki/Architecture) page for how the pieces fit together.

## Read This First

This repository is **personal infrastructure tooling** for how I run my homelab. It is not intended for a general audience, likely will not work for you as-is, and may rely on assumptions, configs, and workflows that only exist in my environment. Use it only as a reference or starting point.

## High-Level Overview

yeet is a lightweight client + server setup for deploying and managing services on remote Linux machines. The primary use case is running Docker images on a host over Tailscale with a tiny workflow (`yeet run <svc> <image>`).

- Run Docker images or Compose stacks on a remote host
- Push locally-built images into an internal registry when you need them
- Manage service lifecycle (start/stop/restart/logs/status)
- Push updates over Tailscale RPC
- Support a few networking modes used in my lab (e.g., Tailscale, macvlan)

## Docker Quickstart (Most Common Path)

```bash
yeet init root@<host>
yeet run <svc> nginx:latest
```

Other common variants:

```bash
yeet run <svc> ./compose.yml
yeet run <svc> ./Dockerfile
yeet push <svc> <local-image>:<tag> --run
```

## Documentation (Wiki)

The wiki is the user manual and the source of truth for behavior and workflows:

- [Quick Start](https://github.com/shayne/yeet/wiki/Quick-Start)
- [Workflows](https://github.com/shayne/yeet/wiki/Workflows) (Docker-first walkthroughs)
- [Installation](https://github.com/shayne/yeet/wiki/Installation)
- [Architecture](https://github.com/shayne/yeet/wiki/Architecture)
- [CLI Overview](https://github.com/shayne/yeet/wiki/CLI-Overview)
- [yeet CLI](https://github.com/shayne/yeet/wiki/Yeet-CLI)
- [catch CLI](https://github.com/shayne/yeet/wiki/Catch-CLI)
- [Networking](https://github.com/shayne/yeet/wiki/Networking)
- [Service Types](https://github.com/shayne/yeet/wiki/Service-Types)
- [Configuration & Prefs](https://github.com/shayne/yeet/wiki/Configuration-and-Prefs)
- [Data Layout](https://github.com/shayne/yeet/wiki/Data-Layout)
- [Troubleshooting](https://github.com/shayne/yeet/wiki/Troubleshooting)
- [Development](https://github.com/shayne/yeet/wiki/Development)
- [FAQ](https://github.com/shayne/yeet/wiki/FAQ)

## Components

- **yeet**: client CLI used from my workstation (see the [yeet CLI](https://github.com/shayne/yeet/wiki/Yeet-CLI) reference)
- **catch**: service manager daemon running on homelab hosts (see the [catch CLI](https://github.com/shayne/yeet/wiki/Catch-CLI) reference)

## How I Run It

In my homelab, I run `catch` on each host and use `yeet` to push binaries/images, manage versions, and poke at service state over Tailscale. The [Networking](https://github.com/shayne/yeet/wiki/Networking) and [Configuration & Prefs](https://github.com/shayne/yeet/wiki/Configuration-and-Prefs) pages describe the host targeting and network modes that make this work in my lab. The workflow is optimized for my machines and my network topology, not for general compatibility.

## Security Notes

Currently, services managed by `catch` run as root. This is fine for my lab, but it is not a good default for production or multi-tenant setups. See the [FAQ](https://github.com/shayne/yeet/wiki/FAQ) for current limitations.

## License

BSD 3-Clause. See `LICENSE`.
