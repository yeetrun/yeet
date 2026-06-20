# Yeet CLI --help-llm Outputs

Generated from this repo using:

```bash
tools/generate-yeet-help-llm.sh
```

If command behavior changes, rerun the generator and commit the updated
reference.

## Top-level

````
# yeet CLI Reference

Deploy/manage services on a remote catch host; commands go over RPC.

## Usage

```
yeet [GLOBAL_OPTIONS] COMMAND [OPTIONS] [ARGS...]
```

## Global Options

These options can be used with any command:

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Commands

### `copy`

Copy files between local paths and service data or VM guests

**Aliases**: `cp`

**Examples**:

```
yeet copy ./config.yml svc:data/config.yml
```

```
yeet copy ./configs/*.yml devbox:~/configs/
```

```
yeet copy devbox:"/var/log/*.log" ./logs/
```

```
yeet copy --force-proxy ./configs/ devbox:~/configs/
```

Get detailed help: `yeet copy --help-llm`

### `cron`

Install a cron job from a file and 5-field expression

**Examples**:

```
yeet cron <svc> ./job.sh "0 9 * * *" -- --job-arg foo
```

Get detailed help: `yeet cron --help-llm`

### `disable`

Disable a service

Get detailed help: `yeet disable --help-llm`

### `edit`

Edit a service

Get detailed help: `yeet edit --help-llm`

### `enable`

Enable a service

Get detailed help: `yeet enable --help-llm`

### `events`

Show events for a service

Get detailed help: `yeet events --help-llm`

### `info`

Show detailed info about a service, including published ports

Get detailed help: `yeet info --help-llm`

### `init`

Install catch on a remote host (prompts for Tailscale OAuth setup when needed)

**Examples**:

```
yeet init root@<machine-host>
```

```
yeet init --install-docker root@<machine-host>
```

```
yeet init --install-docker --install-vm-tools root@<machine-host>
```

```
yeet init --ts-client-secret=<secret> root@<machine-host>
```

```
yeet init --ts-auth-key=<key> root@<machine-host>
```

```
yeet init
```

Get detailed help: `yeet init --help-llm`

### `ip`

Show the IP addresses of a service

Get detailed help: `yeet ip --help-llm`

### `list-hosts`

List tailnet hosts with the given tags; requires a local Tailscale client

Get detailed help: `yeet list-hosts --help-llm`

### `logs`

Show logs of a service

Get detailed help: `yeet logs --help-llm`

### `mount`

Mount a network filesystem on the host (global, not per-service)

**Examples**:

```
yeet mount host:/export data-share --type=nfs --opts=defaults
```

```
yeet mount
```

Get detailed help: `yeet mount --help-llm`

### `prefs`

Manage the current preferences

Get detailed help: `yeet prefs --help-llm`

### `remove`

Remove a service

**Aliases**: `rm`

Get detailed help: `yeet remove --help-llm`

### `restart`

Restart a service

Get detailed help: `yeet restart --help-llm`

### `run`

Install/update from a payload (binary, compose, image, Dockerfile, VM)

**Examples**:

```
yeet run --web
```

```
yeet run --web <svc>
```

```
yeet run --web <svc> ./compose.yml
```

```
yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
yeet run -p 80:80 <svc> nginx:latest
```

```
yeet run --publish-reset -p 443:443 <svc> nginx:latest
```

```
yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
```

```
yeet run <svc> vm://ubuntu/26.04 --net=svc
```

```
yeet run <svc> vm://nixos/26.05
```

```
yeet run <svc> vm://ubuntu/26.04 --image-policy=update
```

```
yeet run <svc> ./compose.yml --service-root=tank/apps/<svc> --zfs
```

```
yeet run <svc> ./compose.yml --snapshots=off
```

```
yeet run --pull <svc> ./compose.yml
```

```
yeet run --force <svc> ./compose.yml
```

```
yeet run --env-file=prod.env <svc> ./compose.yml
```

```
yeet run <svc> ghcr.io/org/app:latest
```

```
yeet run <svc> ./Dockerfile
```

Get detailed help: `yeet run --help-llm`

### `ssh`

Open SSH to the catch host, a service dir, or a VM guest

**Examples**:

```
yeet ssh
```

```
yeet --host=<host> ssh
```

```
yeet ssh <svc>
```

```
yeet ssh --force-proxy <vm>
```

```
yeet ssh -- uname -a
```

```
yeet ssh <svc> -- ls -la
```

Get detailed help: `yeet ssh --help-llm`

### `stage`

Upload a payload without applying it (use stage show/commit/clear)

**Examples**:

```
yeet stage <svc> ./bin/<svc>
```

```
yeet stage <svc> show
```

```
yeet stage <svc> commit
```

```
yeet stage <svc> clear
```

Get detailed help: `yeet stage --help-llm`

### `start`

Start a service

Get detailed help: `yeet start --help-llm`

### `status`

Show status of a service

Get detailed help: `yeet status --help-llm`

### `stop`

Stop a service

Get detailed help: `yeet stop --help-llm`

### `tailscale`

Configure tailscale OAuth or run tailscale commands in a service netns

**Aliases**: `ts`

**Examples**:

```
yeet tailscale --setup
```

```
yeet tailscale --setup --client-secret=tskey-client-***
```

```
yeet tailscale <svc> -- serve --bg 8080
```

Get detailed help: `yeet tailscale --help-llm`

### `umount`

Unmount a host mount by name

**Examples**:

```
yeet umount data-share
```

Get detailed help: `yeet umount --help-llm`

### `upgrade`

Check for and install yeet/catch updates

**Examples**:

```
yeet upgrade check
```

```
yeet upgrade check --all
```

```
yeet upgrade --all
```

```
yeet upgrade --all --force
```

```
yeet upgrade --all --version v0.6.1 --force
```

Get detailed help: `yeet upgrade --help-llm`

### `version`

Show the version of the Catch server

Get detailed help: `yeet version --help-llm`

## Command Groups

### `docker`

Docker compose and registry management

**Commands**:

- `docker outdated`: Show Docker compose containers with upstream image updates
- `docker pull`: Pull images for a compose service without restarting
- `docker push`: Push a container image to the remote host (optionally run it)
- `docker update`: Pull images and recreate containers for compose services

Get detailed help: `yeet docker --help-llm`

### `env`

Manage service environment files

**Commands**:

- `env copy`: Upload an env file (alias: cp)
- `env edit`: Edit the env file
- `env set`: Set env keys
- `env show`: Print the current env file

Get detailed help: `yeet env --help-llm`

### `service`

Manage service settings

**Commands**:

- `service generations`: service generations <svc> [--format=table|json|json-pretty] - Show service generation rollback state
- `service rollback`: service rollback <svc> - Rollback a service to the previous generation
- `service set`: Set service settings
- `service sync`: Sync local yeet.toml service settings from catch

Get detailed help: `yeet service --help-llm`

### `snapshots`

Manage service recovery points and snapshot defaults

**Commands**:

- `snapshots clone`: Clone a recovery point to a new service
- `snapshots create`: Create a manual recovery point
- `snapshots defaults`: Show or set catch snapshot defaults
- `snapshots inspect`: Inspect one recovery point
- `snapshots list`: List yeet recovery points
- `snapshots protect`: Protect a recovery point from retention pruning
- `snapshots restore`: Restore disk state, service-root state, or full VM state from a recovery point
- `snapshots rm`: Delete a yeet recovery point
- `snapshots unprotect`: Allow retention pruning for a recovery point

Get detailed help: `yeet snapshots --help-llm`

### `vm`

Manage VM-specific commands

**Commands**:

- `vm console`: Stream VM serial console output
- `vm images`: Show available VM images and manage VM image cache state
- `vm set`: Set VM resources and networking

Get detailed help: `yeet vm --help-llm`

## Examples

```
yeet status
```

```
yeet status <svc>
```

```
yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
```

## Getting Help

- Global help: `yeet --help-llm`
- Group help: `yeet <group> --help-llm`
- Command help: `yeet <command> --help-llm`
````

## Command: copy

````
# yeet copy

Copy files between local paths and service data or VM guests

## Usage

```
yeet [GLOBAL_OPTIONS] copy [OPTIONS] [--force-proxy] [-avz] <src>... <dst>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet copy ./config.yml svc:data/config.yml
```

```
yeet copy ./configs/*.yml devbox:~/configs/
```

```
yeet copy devbox:"/var/log/*.log" ./logs/
```

```
yeet copy --force-proxy ./configs/ devbox:~/configs/
```
````

## Command: cron

````
# yeet cron

Install a cron job from a file and 5-field expression

## Usage

```
yeet [GLOBAL_OPTIONS] cron <SERVICE> [OPTIONS] FILE "<cron expr>" [-- <args...>]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet cron <svc> ./job.sh "0 9 * * *" -- --job-arg foo
```
````

## Command: disable

````
# yeet disable

Disable a service

## Usage

```
yeet [GLOBAL_OPTIONS] disable <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: edit

````
# yeet edit

Edit a service

## Usage

```
yeet [GLOBAL_OPTIONS] edit <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: enable

````
# yeet enable

Enable a service

## Usage

```
yeet [GLOBAL_OPTIONS] enable <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: events

````
# yeet events

Show events for a service

## Usage

```
yeet [GLOBAL_OPTIONS] events [OPTIONS]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: info

````
# yeet info

Show detailed info about a service, including published ports

## Usage

```
yeet [GLOBAL_OPTIONS] info <SERVICE> [OPTIONS] SVC [--format=plain|json|json-pretty]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: init

````
# yeet init

Install catch on a remote host (prompts for Tailscale OAuth setup when needed)

## Usage

```
yeet [GLOBAL_OPTIONS] init [OPTIONS] [--from-github] [--nightly] [--install-docker] [--install-vm-tools] [--ts-client-secret=<secret>] [--ts-auth-key=<key>] [ROOT@MACHINE-HOST]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet init root@<machine-host>
```

```
yeet init --install-docker root@<machine-host>
```

```
yeet init --install-docker --install-vm-tools root@<machine-host>
```

```
yeet init --ts-client-secret=<secret> root@<machine-host>
```

```
yeet init --ts-auth-key=<key> root@<machine-host>
```

```
yeet init
```
````

## Command: ip

````
# yeet ip

Show the IP addresses of a service

## Usage

```
yeet [GLOBAL_OPTIONS] ip [OPTIONS]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: list-hosts

````
# yeet list-hosts

List tailnet hosts with the given tags; requires a local Tailscale client

## Usage

```
yeet [GLOBAL_OPTIONS] list-hosts [OPTIONS] [--tags=tag:catch]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: logs

````
# yeet logs

Show logs of a service

## Usage

```
yeet [GLOBAL_OPTIONS] logs <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: mount

````
# yeet mount

Mount a network filesystem on the host (global, not per-service)

## Usage

```
yeet [GLOBAL_OPTIONS] mount [OPTIONS] SOURCE [name] [--type=nfs] [--opts=defaults]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet mount host:/export data-share --type=nfs --opts=defaults
```

```
yeet mount
```
````

## Command: prefs

````
# yeet prefs

Manage the current preferences

## Usage

```
yeet [GLOBAL_OPTIONS] prefs [OPTIONS]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: remove

````
# yeet remove

Remove a service

## Usage

```
yeet [GLOBAL_OPTIONS] remove <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Options

### `--yes` (short: `-y`)

Skip the removal prompt

- **Type**: `bool`

### `--clean-config`

Delete the matching yeet.toml entry without prompting

- **Type**: `bool`

### `--clean-data`

Delete service data instead of preserving data/

- **Type**: `bool`

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: restart

````
# yeet restart

Restart a service

## Usage

```
yeet [GLOBAL_OPTIONS] restart <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: run

````
# yeet run

Install/update from a payload (binary, compose, image, Dockerfile, VM)

## Usage

```
yeet [GLOBAL_OPTIONS] run <SERVICE> [OPTIONS] SVC [PAYLOAD] [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--snapshots=on|off|inherit] [-- <payload args>] | --web [SVC] [PAYLOAD]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet run --web
```

```
yeet run --web <svc>
```

```
yeet run --web <svc> ./compose.yml
```

```
yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
yeet run -p 80:80 <svc> nginx:latest
```

```
yeet run --publish-reset -p 443:443 <svc> nginx:latest
```

```
yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
```

```
yeet run <svc> vm://ubuntu/26.04 --net=svc
```

```
yeet run <svc> vm://nixos/26.05
```

```
yeet run <svc> vm://ubuntu/26.04 --image-policy=update
```

```
yeet run <svc> ./compose.yml --service-root=tank/apps/<svc> --zfs
```

```
yeet run <svc> ./compose.yml --snapshots=off
```

```
yeet run --pull <svc> ./compose.yml
```

```
yeet run --force <svc> ./compose.yml
```

```
yeet run --env-file=prod.env <svc> ./compose.yml
```

```
yeet run <svc> ghcr.io/org/app:latest
```

```
yeet run <svc> ./Dockerfile
```
````

## Command: ssh

````
# yeet ssh

Open SSH to the catch host, a service dir, or a VM guest

## Usage

```
yeet [GLOBAL_OPTIONS] ssh [OPTIONS] [--force-proxy] [ssh-opts...] [<svc>] [-- <remote-cmd...>]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet ssh
```

```
yeet --host=<host> ssh
```

```
yeet ssh <svc>
```

```
yeet ssh --force-proxy <vm>
```

```
yeet ssh -- uname -a
```

```
yeet ssh <svc> -- ls -la
```
````

## Command: stage

````
# yeet stage

Upload a payload without applying it (use stage show/commit/clear)

## Usage

```
yeet [GLOBAL_OPTIONS] stage <SERVICE> [OPTIONS] SVC PAYLOAD|show|commit|clear [-- <payload args>]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet stage <svc> ./bin/<svc>
```

```
yeet stage <svc> show
```

```
yeet stage <svc> commit
```

```
yeet stage <svc> clear
```
````

## Command: start

````
# yeet start

Start a service

## Usage

```
yeet [GLOBAL_OPTIONS] start <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: status

````
# yeet status

Show status of a service

## Usage

```
yeet [GLOBAL_OPTIONS] status [OPTIONS]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: stop

````
# yeet stop

Stop a service

## Usage

```
yeet [GLOBAL_OPTIONS] stop <SERVICE> [OPTIONS]
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Command: tailscale

````
# yeet tailscale

Configure tailscale OAuth or run tailscale commands in a service netns

## Usage

```
yeet [GLOBAL_OPTIONS] tailscale <SERVICE> [OPTIONS] --setup [--client-secret=...] | <svc> -- <tailscale args...>
```

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet tailscale --setup
```

```
yeet tailscale --setup --client-secret=tskey-client-***
```

```
yeet tailscale <svc> -- serve --bg 8080
```
````

## Command: umount

````
# yeet umount

Unmount a host mount by name

## Usage

```
yeet [GLOBAL_OPTIONS] umount [OPTIONS] NAME
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet umount data-share
```
````

## Command: upgrade

````
# yeet upgrade

Check for and install yeet/catch updates

## Usage

```
yeet [GLOBAL_OPTIONS] upgrade [OPTIONS] [check] [--all] [--host=catch-a] [--json] [--yes] [--force] [--version=vX.Y.Z]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet upgrade check
```

```
yeet upgrade check --all
```

```
yeet upgrade --all
```

```
yeet upgrade --all --force
```

```
yeet upgrade --all --version v0.6.1 --force
```
````

## Command: version

````
# yeet version

Show the version of the Catch server

## Usage

```
yeet [GLOBAL_OPTIONS] version [OPTIONS]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group: docker

````
# yeet - docker

Docker compose and registry management

## Usage

```
yeet [GLOBAL_OPTIONS] docker COMMAND [OPTIONS] [ARGS...]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Commands

### `docker outdated`

Show Docker compose containers with upstream image updates

**Examples**:

```
yeet docker outdated
```

```
yeet docker outdated <svc>
```

```
yeet docker outdated --format=json
```

Get detailed help: `yeet docker outdated --help-llm`

### `docker pull`

Pull images for a compose service without restarting

Get detailed help: `yeet docker pull --help-llm`

### `docker push`

Push a container image to the remote host (optionally run it)

**Examples**:

```
yeet docker push <svc> <local-image>:<tag> --run
```

Get detailed help: `yeet docker push --help-llm`

### `docker update`

Pull images and recreate containers for compose services

**Examples**:

```
yeet docker update <svc>
```

```
yeet docker update <svc-a> <svc-b>
```

```
yeet docker update <svc-a> <svc-b>@<host>
```

```
yeet docker update --outdated
```

Get detailed help: `yeet docker update --help-llm`
````

## Group: env

````
# yeet - env

Manage service environment files

## Usage

```
yeet [GLOBAL_OPTIONS] env COMMAND [OPTIONS] [ARGS...]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Commands

### `env copy`

Upload an env file

Get detailed help: `yeet env copy --help-llm`

### `env edit`

Edit the env file

Get detailed help: `yeet env edit --help-llm`

### `env set`

Set env keys

Get detailed help: `yeet env set --help-llm`

### `env show`

Print the current env file

Get detailed help: `yeet env show --help-llm`
````

## Group: service

````
# yeet - service

Manage service settings

## Usage

```
yeet [GLOBAL_OPTIONS] service COMMAND [OPTIONS] [ARGS...]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Commands

### `service generations`

service generations <svc> [--format=table|json|json-pretty] - Show service generation rollback state

Get detailed help: `yeet service generations --help-llm`

### `service rollback`

service rollback <svc> - Rollback a service to the previous generation

Get detailed help: `yeet service rollback --help-llm`

### `service set`

Set service settings

**Examples**:

```
yeet service set <svc> -p 80:80 -p 443:443
```

```
yeet service set <svc> --publish-reset -p 443:443
```

```
yeet service set <svc> --publish-reset
```

```
yeet service set <svc> --service-root=/srv/apps/<svc>
```

```
yeet service set <svc> --service-root=tank/apps/<svc> --zfs --copy
```

```
yeet service set <svc> --service-root=/srv/apps/<svc> --empty
```

```
yeet service set <svc> --snapshots=off
```

```
yeet service set <svc> --snapshots=on --snapshot-keep-last=5 --snapshot-max-age=7d
```

Get detailed help: `yeet service set --help-llm`

### `service sync`

Sync local yeet.toml service settings from catch

**Examples**:

```
yeet service sync <svc>
```

```
yeet service sync --all
```

```
yeet service sync <svc> --config ~/yeet-services/yeet.toml
```

Get detailed help: `yeet service sync --help-llm`
````

## Group: snapshots

````
# yeet - snapshots

Manage service recovery points and snapshot defaults

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots COMMAND [OPTIONS] [ARGS...]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Commands

### `snapshots clone`

Clone a recovery point to a new service

**Examples**:

```
yeet snapshots clone <svc> yeet-20260613T203100Z-vm-manual-g0 <new-svc>
```

Get detailed help: `yeet snapshots clone --help-llm`

### `snapshots create`

Create a manual recovery point

**Examples**:

```
yeet snapshots create <svc>
```

```
yeet snapshots create <svc> --comment="before upgrade"
```

```
yeet snapshots create <vm> --full --comment="checkpoint before risky change"
```

Get detailed help: `yeet snapshots create --help-llm`

### `snapshots defaults`

Show or set catch snapshot defaults

**Examples**:

```
yeet snapshots defaults show
```

```
yeet snapshots defaults set --enabled=false
```

```
yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d
```

Get detailed help: `yeet snapshots defaults --help-llm`

### `snapshots inspect`

Inspect one recovery point

**Examples**:

```
yeet snapshots inspect <svc> yeet-20260613T203100Z-vm-manual-g0
```

```
yeet snapshots inspect <svc> yeet-20260613 --format=json
```

Get detailed help: `yeet snapshots inspect --help-llm`

### `snapshots list`

List yeet recovery points

**Examples**:

```
yeet snapshots list
```

```
yeet snapshots list <svc>
```

```
yeet snapshots list <svc> --format=json
```

Get detailed help: `yeet snapshots list --help-llm`

### `snapshots protect`

Protect a recovery point from retention pruning

**Examples**:

```
yeet snapshots protect <svc> yeet-20260613T203100Z-vm-manual-g0
```

Get detailed help: `yeet snapshots protect --help-llm`

### `snapshots restore`

Restore disk state, service-root state, or full VM state from a recovery point

**Examples**:

```
yeet snapshots restore <svc> yeet-20260613T203100Z-vm-manual-g0 --yes
```

```
yeet snapshots restore <svc> yeet-20260613 --stop --yes
```

```
yeet snapshots restore <vm> yeet-20260613T203100Z-vm-manual --mode=full --stop --yes
```

Get detailed help: `yeet snapshots restore --help-llm`

### `snapshots rm`

Delete a yeet recovery point

**Examples**:

```
yeet snapshots rm <svc> yeet-20260613T203100Z-vm-manual-g0
```

Get detailed help: `yeet snapshots rm --help-llm`

### `snapshots unprotect`

Allow retention pruning for a recovery point

**Examples**:

```
yeet snapshots unprotect <svc> yeet-20260613T203100Z-vm-manual-g0
```

Get detailed help: `yeet snapshots unprotect --help-llm`
````

## Group: vm

````
# yeet - vm

Manage VM-specific commands

## Usage

```
yeet [GLOBAL_OPTIONS] vm COMMAND [OPTIONS] [ARGS...]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Commands

### `vm console`

Stream VM serial console output

Get detailed help: `yeet vm console --help-llm`

### `vm images`

Show available VM images and manage VM image cache state

**Examples**:

```
yeet vm images catalog
```

```
yeet vm images
```

```
yeet vm images ls
```

```
yeet vm images update
```

```
yeet vm images update vm://nixos/26.05
```

```
yeet vm images import foo/bar ./dist/my-vm
```

```
yeet vm images import kernel/test ./dist/my-vm --allow-local-kernel
```

```
yeet vm images rm foo/bar --yes
```

```
yeet vm images prune
```

```
yeet vm images prune --dry-run
```

Get detailed help: `yeet vm images --help-llm`

### `vm set`

Set VM resources and networking

**Examples**:

```
yeet vm set <vm> --cpus=8 --memory=8g --disk=128g
```

```
yeet vm set <vm> --net=lan
```

```
yeet vm set <vm> --net=svc,lan --macvlan-parent=vmbr0 --macvlan-vlan=4
```

Get detailed help: `yeet vm set --help-llm`
````

## Group Command: docker outdated

````
# yeet docker outdated

Show Docker compose containers with upstream image updates

## Usage

```
yeet [GLOBAL OPTIONS] docker outdated [SVC] [--format=table|json|json-pretty]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet docker outdated
```

```
yeet docker outdated <svc>
```

```
yeet docker outdated --format=json
```
````

## Group Command: docker pull

````
# yeet docker pull

Pull images for a compose service without restarting

## Usage

```
yeet [GLOBAL OPTIONS] docker pull <svc>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: docker push

````
# yeet docker push

Push a container image to the remote host (optionally run it)

## Usage

```
yeet [GLOBAL OPTIONS] docker push SVC IMAGE [--run] [--all-local]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet docker push <svc> <local-image>:<tag> --run
```
````

## Group Command: docker update

````
# yeet docker update

Pull images and recreate containers for compose services

## Usage

```
yeet [GLOBAL OPTIONS] docker update <svc...> | docker update --outdated
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet docker update <svc>
```

```
yeet docker update <svc-a> <svc-b>
```

```
yeet docker update <svc-a> <svc-b>@<host>
```

```
yeet docker update --outdated
```
````

## Group Command: env copy

````
# yeet env copy

Upload an env file

## Usage

```
yeet [GLOBAL OPTIONS] env copy <svc> <file>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: env edit

````
# yeet env edit

Edit the env file

## Usage

```
yeet [GLOBAL OPTIONS] env edit <svc>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: env set

````
# yeet env set

Set env keys

## Usage

```
yeet [GLOBAL OPTIONS] env set <svc> KEY=VALUE [KEY=VALUE...]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: env show

````
# yeet env show

Print the current env file

## Usage

```
yeet [GLOBAL OPTIONS] env show <svc> [--staged]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: service generations

````
# yeet service generations

service generations <svc> [--format=table|json|json-pretty] - Show service generation rollback state

## Usage

```
yeet [GLOBAL OPTIONS] service generations <svc> [--format=table|json|json-pretty]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: service rollback

````
# yeet service rollback

service rollback <svc> - Rollback a service to the previous generation

## Usage

```
yeet [GLOBAL OPTIONS] service rollback <svc>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: service set

````
# yeet service set

Set service settings

## Usage

```
yeet [GLOBAL OPTIONS] service set <svc> [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit] [--snapshot-keep-last=N] [--snapshot-max-age=7d] [--snapshot-events=run,docker-update] [--snapshot-required=true|false]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet service set <svc> -p 80:80 -p 443:443
```

```
yeet service set <svc> --publish-reset -p 443:443
```

```
yeet service set <svc> --publish-reset
```

```
yeet service set <svc> --service-root=/srv/apps/<svc>
```

```
yeet service set <svc> --service-root=tank/apps/<svc> --zfs --copy
```

```
yeet service set <svc> --service-root=/srv/apps/<svc> --empty
```

```
yeet service set <svc> --snapshots=off
```

```
yeet service set <svc> --snapshots=on --snapshot-keep-last=5 --snapshot-max-age=7d
```
````

## Group Command: service sync

````
# yeet service sync

Sync local yeet.toml service settings from catch

## Usage

```
yeet [GLOBAL OPTIONS] service sync <svc> [--config=PATH] | service sync --all [--config=PATH]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet service sync <svc>
```

```
yeet service sync --all
```

```
yeet service sync <svc> --config ~/yeet-services/yeet.toml
```
````

## Group Command: snapshots clone

````
# yeet snapshots clone

Clone a recovery point to a new service

## Usage

```
yeet [GLOBAL OPTIONS] snapshots clone <svc> <snapshot> <new-svc>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots clone <svc> yeet-20260613T203100Z-vm-manual-g0 <new-svc>
```
````

## Group Command: snapshots create

````
# yeet snapshots create

Create a manual recovery point

## Usage

```
yeet [GLOBAL OPTIONS] snapshots create <svc> [--comment=TEXT] [--full]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots create <svc>
```

```
yeet snapshots create <svc> --comment="before upgrade"
```

```
yeet snapshots create <vm> --full --comment="checkpoint before risky change"
```
````

## Group Command: snapshots defaults

````
# yeet snapshots defaults

Show or set catch snapshot defaults

## Usage

```
yeet [GLOBAL OPTIONS] snapshots defaults show | snapshots defaults set [--enabled=true|false] [--keep-last=N] [--max-age=7d] [--events=run,docker-update] [--required=true|false]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots defaults show
```

```
yeet snapshots defaults set --enabled=false
```

```
yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d
```
````

## Group Command: snapshots inspect

````
# yeet snapshots inspect

Inspect one recovery point

## Usage

```
yeet [GLOBAL OPTIONS] snapshots inspect <svc> <snapshot> [--format=table|json|json-pretty]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots inspect <svc> yeet-20260613T203100Z-vm-manual-g0
```

```
yeet snapshots inspect <svc> yeet-20260613 --format=json
```
````

## Group Command: snapshots list

````
# yeet snapshots list

List yeet recovery points

## Usage

```
yeet [GLOBAL OPTIONS] snapshots list [svc] [--format=table|json|json-pretty]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots list
```

```
yeet snapshots list <svc>
```

```
yeet snapshots list <svc> --format=json
```
````

## Group Command: snapshots protect

````
# yeet snapshots protect

Protect a recovery point from retention pruning

## Usage

```
yeet [GLOBAL OPTIONS] snapshots protect <svc> <snapshot>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots protect <svc> yeet-20260613T203100Z-vm-manual-g0
```
````

## Group Command: snapshots restore

````
# yeet snapshots restore

Restore disk state, service-root state, or full VM state from a recovery point

## Usage

```
yeet [GLOBAL OPTIONS] snapshots restore <svc> <snapshot> [--stop] [--start] [--yes] [--mode=disk|full] [--generation=current|snapshot]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots restore <svc> yeet-20260613T203100Z-vm-manual-g0 --yes
```

```
yeet snapshots restore <svc> yeet-20260613 --stop --yes
```

```
yeet snapshots restore <vm> yeet-20260613T203100Z-vm-manual --mode=full --stop --yes
```
````

## Group Command: snapshots rm

````
# yeet snapshots rm

Delete a yeet recovery point

## Usage

```
yeet [GLOBAL OPTIONS] snapshots rm <svc> <snapshot> [--yes]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots rm <svc> yeet-20260613T203100Z-vm-manual-g0
```
````

## Group Command: snapshots unprotect

````
# yeet snapshots unprotect

Allow retention pruning for a recovery point

## Usage

```
yeet [GLOBAL OPTIONS] snapshots unprotect <svc> <snapshot>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet snapshots unprotect <svc> yeet-20260613T203100Z-vm-manual-g0
```
````

## Group Command: vm console

````
# yeet vm console

Stream VM serial console output

## Usage

```
yeet [GLOBAL OPTIONS] vm console <svc>
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`
````

## Group Command: vm images

````
# yeet vm images

Show available VM images and manage VM image cache state

## Usage

```
yeet [GLOBAL OPTIONS] vm images [ls|catalog|update|import <name> <dir>|rm <name>|prune] [--format=table|json|json-pretty]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet vm images catalog
```

```
yeet vm images
```

```
yeet vm images ls
```

```
yeet vm images update
```

```
yeet vm images update vm://nixos/26.05
```

```
yeet vm images import foo/bar ./dist/my-vm
```

```
yeet vm images import kernel/test ./dist/my-vm --allow-local-kernel
```

```
yeet vm images rm foo/bar --yes
```

```
yeet vm images prune
```

```
yeet vm images prune --dry-run
```
````

## Group Command: vm set

````
# yeet vm set

Set VM resources and networking

## Usage

```
yeet [GLOBAL OPTIONS] vm set <vm> [--cpus=N] [--memory=SIZE] [--disk=SIZE] [--net=svc|lan|svc,lan] [--macvlan-parent=IFACE] [--macvlan-vlan=ID] [--macvlan-mac=MAC]
```

## Global Options

### `--host`

Override target host (CATCH_HOST)

- **Type**: `string`

### `--service`

Force the service name for the command

- **Type**: `string`

### `--tty`

Force TTY for remote commands

- **Type**: `bool`

### `--no-tty`

Disable TTY for remote commands

- **Type**: `bool`

### `--progress`

Progress output (auto|tty|plain|quiet)

- **Type**: `string`

## Examples

```
yeet vm set <vm> --cpus=8 --memory=8g --disk=128g
```

```
yeet vm set <vm> --net=lan
```

```
yeet vm set <vm> --net=svc,lan --macvlan-parent=vmbr0 --macvlan-vlan=4
```
````
