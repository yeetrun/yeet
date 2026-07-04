# Yeet CLI --help-agent Outputs

Generated from this repo using:

```bash
tools/generate-yeet-help-agent.sh
```

If command behavior changes, rerun the generator and commit the updated
reference.

## Top-level

````
# yeet Agent Context

## Purpose

Deploy/manage services on a remote catch host; commands go over RPC.

## Usage

```
yeet [GLOBAL_OPTIONS] COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet copy --help-agent` for command-specific context.
- Run `yeet cron --help-agent` for command-specific context.
- Run `yeet disable --help-agent` for command-specific context.
- Run `yeet edit --help-agent` for command-specific context.
- Run `yeet enable --help-agent` for command-specific context.
- Run `yeet events --help-agent` for command-specific context.
- Run `yeet info --help-agent` for command-specific context.
- Run `yeet init --help-agent` for command-specific context.
- Run `yeet ip --help-agent` for command-specific context.
- Run `yeet list-hosts --help-agent` for command-specific context.
- Run `yeet logs --help-agent` for command-specific context.
- Run `yeet mount --help-agent` for command-specific context.
- Run `yeet prefs --help-agent` for command-specific context.
- Run `yeet remove --help-agent` for command-specific context.
- Run `yeet restart --help-agent` for command-specific context.
- Run `yeet run --help-agent` for command-specific context.
- Run `yeet ssh --help-agent` for command-specific context.
- Run `yeet stage --help-agent` for command-specific context.
- Run `yeet start --help-agent` for command-specific context.
- Run `yeet status --help-agent` for command-specific context.
- Run `yeet stop --help-agent` for command-specific context.
- Run `yeet tailscale --help-agent` for command-specific context.
- Run `yeet umount --help-agent` for command-specific context.
- Run `yeet upgrade --help-agent` for command-specific context.
- Run `yeet version --help-agent` for command-specific context.
- Run `yeet docker --help-agent` for group-specific context.
- Run `yeet env --help-agent` for group-specific context.
- Run `yeet host --help-agent` for group-specific context.
- Run `yeet service --help-agent` for group-specific context.
- Run `yeet snapshots --help-agent` for group-specific context.
- Run `yeet vm --help-agent` for group-specific context.

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

### `copy`

Copy files between local paths and service data or VM guests

**Aliases**: `cp`

Run `yeet copy --help-agent` for command-specific context.

### `cron`

Install a cron job from a file and 5-field expression

Run `yeet cron --help-agent` for command-specific context.

### `disable`

Disable a service

Run `yeet disable --help-agent` for command-specific context.

### `edit`

Edit a service

Run `yeet edit --help-agent` for command-specific context.

### `enable`

Enable a service

Run `yeet enable --help-agent` for command-specific context.

### `events`

Show events for a service

Run `yeet events --help-agent` for command-specific context.

### `info`

Show host info, or detailed service info when SVC is supplied

Run `yeet info --help-agent` for command-specific context.

### `init`

Install catch on a remote host (prompts for Tailscale OAuth setup when needed)

Run `yeet init --help-agent` for command-specific context.

### `ip`

Show the connectable IP endpoints of a service

Run `yeet ip --help-agent` for command-specific context.

### `list-hosts`

List tailnet hosts with the given tags; requires a local Tailscale client

Run `yeet list-hosts --help-agent` for command-specific context.

### `logs`

Show logs of a service

Run `yeet logs --help-agent` for command-specific context.

### `mount`

Mount a network filesystem on the host (global, not per-service)

Run `yeet mount --help-agent` for command-specific context.

### `prefs`

Manage the current preferences

Run `yeet prefs --help-agent` for command-specific context.

### `remove`

Remove a service

**Aliases**: `rm`

Run `yeet remove --help-agent` for command-specific context.

### `restart`

Restart a service

Run `yeet restart --help-agent` for command-specific context.

### `run`

Install/update from a payload (binary, compose, image, Dockerfile, VM)

Run `yeet run --help-agent` for command-specific context.

### `ssh`

Open a catch host shell, a service shell, or a VM guest shell

Run `yeet ssh --help-agent` for command-specific context.

### `stage`

Upload a payload without applying it (use stage show/commit/clear)

Run `yeet stage --help-agent` for command-specific context.

### `start`

Start a service

Run `yeet start --help-agent` for command-specific context.

### `status`

Show host or service status

Run `yeet status --help-agent` for command-specific context.

### `stop`

Stop a service

Run `yeet stop --help-agent` for command-specific context.

### `tailscale`

Configure tailscale OAuth or run tailscale commands in a service netns

**Aliases**: `ts`

Run `yeet tailscale --help-agent` for command-specific context.

### `umount`

Unmount a host mount by name

Run `yeet umount --help-agent` for command-specific context.

### `upgrade`

Check for and install yeet/catch updates

Run `yeet upgrade --help-agent` for command-specific context.

### `version`

Show the version of the Catch server

Run `yeet version --help-agent` for command-specific context.

## Command Groups

### `docker`

Docker compose and registry management

Run `yeet docker --help-agent` for group-specific context.

### `env`

Manage service environment files

Run `yeet env --help-agent` for group-specific context.

### `host`

Manage catch host settings

Run `yeet host --help-agent` for group-specific context.

### `service`

Manage service settings

Run `yeet service --help-agent` for group-specific context.

### `snapshots`

Manage service recovery points and snapshot defaults

Run `yeet snapshots --help-agent` for group-specific context.

### `vm`

Manage VM-specific commands

Run `yeet vm --help-agent` for group-specific context.

## Examples

```
yeet status
```

```
yeet status <svc>
```

```
yeet status <svc-a> <svc-b>
```

```
yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
```
````

## Command: copy

````
# yeet copy Agent Context

## Purpose

Copy files between local paths and service data or VM guests

**Aliases**: `cp`

## Usage

```
yeet [GLOBAL_OPTIONS] copy [--force-proxy] [-avz] <src>... <dst>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet cron Agent Context

## Purpose

Install a cron job from a file and 5-field expression

## Usage

```
yeet [GLOBAL_OPTIONS] cron <SERVICE> FILE "<cron expr>" [-- <args...>]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet disable Agent Context

## Purpose

Disable a service

## Usage

```
yeet [GLOBAL_OPTIONS] disable <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet edit Agent Context

## Purpose

Edit a service

## Usage

```
yeet [GLOBAL_OPTIONS] edit <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet enable Agent Context

## Purpose

Enable a service

## Usage

```
yeet [GLOBAL_OPTIONS] enable <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet events Agent Context

## Purpose

Show events for a service

## Usage

```
yeet [GLOBAL_OPTIONS] events [OPTIONS]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet info Agent Context

## Purpose

Show host info, or detailed service info when SVC is supplied

## Usage

```
yeet [GLOBAL_OPTIONS] info [SVC] [--format=plain|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: false

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
# yeet init Agent Context

## Purpose

Install catch on a remote host (prompts for Tailscale OAuth setup when needed)

## Usage

```
yeet [GLOBAL_OPTIONS] init [--from-github] [--nightly] [--install-docker] [--install-vm-tools] [--data-dir=PATH_OR_DATASET] [--services-root=PATH_OR_DATASET] [--zfs] [--ts-client-secret=<secret>] [--ts-auth-key=<key>] [ROOT@MACHINE-HOST]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
yeet init --data-dir=/srv/yeet-data root@<machine-host>
```

```
yeet init --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services root@<machine-host>
```

```
yeet init --ts-client-secret=<secret> root@<machine-host>
```

```
yeet init --install-docker root@<machine-host>
```

```
yeet init --install-docker --install-vm-tools root@<machine-host>
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
# yeet ip Agent Context

## Purpose

Show the connectable IP endpoints of a service

## Usage

```
yeet [GLOBAL_OPTIONS] ip [OPTIONS]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet list-hosts Agent Context

## Purpose

List tailnet hosts with the given tags; requires a local Tailscale client

## Usage

```
yeet [GLOBAL_OPTIONS] list-hosts [--tags=tag:catch]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet logs Agent Context

## Purpose

Show logs of a service

## Usage

```
yeet [GLOBAL_OPTIONS] logs <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet mount Agent Context

## Purpose

Mount a network filesystem on the host (global, not per-service)

## Usage

```
yeet [GLOBAL_OPTIONS] mount SOURCE [name] [--type=nfs] [--opts=defaults]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet prefs Agent Context

## Purpose

Manage the current preferences

## Usage

```
yeet [GLOBAL_OPTIONS] prefs [OPTIONS]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet remove Agent Context

## Purpose

Remove a service

**Aliases**: `rm`

## Usage

```
yeet [GLOBAL_OPTIONS] remove <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Options

### `--clean`

Delete service data and the matching yeet.toml entry

- **Type**: `bool`

### `--yes` (short: `-y`)

Skip removal prompts; does not imply --clean or --clean-data

- **Type**: `bool`

### `--clean-config`

Delete the matching yeet.toml entry without prompting

- **Type**: `bool`

### `--clean-data`

Delete service data; skips the data-deletion prompt

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
# yeet restart Agent Context

## Purpose

Restart a service

## Usage

```
yeet [GLOBAL_OPTIONS] restart <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet run Agent Context

## Purpose

Install/update from a payload (binary, compose, image, Dockerfile, VM)

## Usage

```
yeet [GLOBAL_OPTIONS] run SVC [PAYLOAD] [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--snapshots=on|off|inherit] [-- <payload args>] | --web [SVC] [PAYLOAD]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
yeet run <svc> vm://ubuntu/26.04 --net=lan
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
# yeet ssh Agent Context

## Purpose

Open a catch host shell, a service shell, or a VM guest shell

## Usage

```
yeet [GLOBAL_OPTIONS] ssh [--force-proxy] [<svc>] [-- <remote-cmd...>]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet stage Agent Context

## Purpose

Upload a payload without applying it (use stage show/commit/clear)

## Usage

```
yeet [GLOBAL_OPTIONS] stage SVC PAYLOAD|show|commit|clear [-- <payload args>]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet start Agent Context

## Purpose

Start a service

## Usage

```
yeet [GLOBAL_OPTIONS] start <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet status Agent Context

## Purpose

Show host or service status

## Usage

```
yeet [GLOBAL_OPTIONS] status [SVC...] [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
yeet status
```

```
yeet status <svc>
```

```
yeet status <svc-a> <svc-b>
```

```
yeet status <svc>@<catch-host>
```
````

## Command: stop

````
# yeet stop Agent Context

## Purpose

Stop a service

## Usage

```
yeet [GLOBAL_OPTIONS] stop <SERVICE>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet tailscale Agent Context

## Purpose

Configure tailscale OAuth or run tailscale commands in a service netns

**Aliases**: `ts`

## Usage

```
yeet [GLOBAL_OPTIONS] tailscale <SERVICE> --setup [--client-secret=...] | <svc> -- <tailscale args...>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet umount Agent Context

## Purpose

Unmount a host mount by name

## Usage

```
yeet [GLOBAL_OPTIONS] umount NAME
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet upgrade Agent Context

## Purpose

Check for and install yeet/catch updates

## Usage

```
yeet [GLOBAL_OPTIONS] upgrade [check] [--host=catch-a] [--json] [--yes] [--force] [--version=vX.Y.Z]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
yeet upgrade
```

```
yeet upgrade --host=catch-a
```

```
yeet upgrade --force
```

```
yeet upgrade --version v0.6.1 --force
```
````

## Command: version

````
# yeet version Agent Context

## Purpose

Show the version of the Catch server

## Usage

```
yeet [GLOBAL_OPTIONS] version [OPTIONS]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet docker Agent Context

## Purpose

Docker compose and registry management

## Usage

```
yeet [GLOBAL_OPTIONS] docker COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet docker outdated --help-agent` for command-specific context.
- Run `yeet docker pull --help-agent` for command-specific context.
- Run `yeet docker push --help-agent` for command-specific context.
- Run `yeet docker update --help-agent` for command-specific context.

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

Run `yeet docker outdated --help-agent` for command-specific context.

### `docker pull`

Pull images for a compose service without restarting

Run `yeet docker pull --help-agent` for command-specific context.

### `docker push`

Push a container image to the remote host (optionally run it)

Run `yeet docker push --help-agent` for command-specific context.

### `docker update`

Pull images and recreate containers for compose services

Run `yeet docker update --help-agent` for command-specific context.
````

## Group: env

````
# yeet env Agent Context

## Purpose

Manage service environment files

## Usage

```
yeet [GLOBAL_OPTIONS] env COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet env copy --help-agent` for command-specific context.
- Run `yeet env edit --help-agent` for command-specific context.
- Run `yeet env set --help-agent` for command-specific context.
- Run `yeet env show --help-agent` for command-specific context.

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

**Aliases**: `cp`

Run `yeet env copy --help-agent` for command-specific context.

### `env edit`

Edit the env file

Run `yeet env edit --help-agent` for command-specific context.

### `env set`

Set env keys

Run `yeet env set --help-agent` for command-specific context.

### `env show`

Print the current env file

Run `yeet env show --help-agent` for command-specific context.
````

## Group: host

````
# yeet host Agent Context

## Purpose

Manage catch host settings

## Usage

```
yeet [GLOBAL_OPTIONS] host COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet host set --help-agent` for command-specific context.

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

### `host set`

Configure catch host storage

Run `yeet host set --help-agent` for command-specific context.
````

## Group: service

````
# yeet service Agent Context

## Purpose

Manage service settings

## Usage

```
yeet [GLOBAL_OPTIONS] service COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet service generations --help-agent` for command-specific context.
- Run `yeet service rollback --help-agent` for command-specific context.
- Run `yeet service set --help-agent` for command-specific context.
- Run `yeet service sync --help-agent` for command-specific context.

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

Run `yeet service generations --help-agent` for command-specific context.

### `service rollback`

service rollback <svc> - Rollback a service to the previous generation

Run `yeet service rollback --help-agent` for command-specific context.

### `service set`

Set service settings

Run `yeet service set --help-agent` for command-specific context.

### `service sync`

Sync local yeet.toml service settings from catch

Run `yeet service sync --help-agent` for command-specific context.
````

## Group: snapshots

````
# yeet snapshots Agent Context

## Purpose

Manage service recovery points and snapshot defaults

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet snapshots clone --help-agent` for command-specific context.
- Run `yeet snapshots create --help-agent` for command-specific context.
- Run `yeet snapshots defaults --help-agent` for command-specific context.
- Run `yeet snapshots inspect --help-agent` for command-specific context.
- Run `yeet snapshots list --help-agent` for command-specific context.
- Run `yeet snapshots protect --help-agent` for command-specific context.
- Run `yeet snapshots restore --help-agent` for command-specific context.
- Run `yeet snapshots rm --help-agent` for command-specific context.
- Run `yeet snapshots unprotect --help-agent` for command-specific context.

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

Run `yeet snapshots clone --help-agent` for command-specific context.

### `snapshots create`

Create a manual recovery point

Run `yeet snapshots create --help-agent` for command-specific context.

### `snapshots defaults`

Show or set catch snapshot defaults

Run `yeet snapshots defaults --help-agent` for command-specific context.

### `snapshots inspect`

Inspect one recovery point

Run `yeet snapshots inspect --help-agent` for command-specific context.

### `snapshots list`

List yeet recovery points

Run `yeet snapshots list --help-agent` for command-specific context.

### `snapshots protect`

Protect a recovery point from retention pruning

Run `yeet snapshots protect --help-agent` for command-specific context.

### `snapshots restore`

Restore disk state, service-root state, or full VM state from a recovery point

Run `yeet snapshots restore --help-agent` for command-specific context.

### `snapshots rm`

Delete a yeet recovery point

Run `yeet snapshots rm --help-agent` for command-specific context.

### `snapshots unprotect`

Allow retention pruning for a recovery point

Run `yeet snapshots unprotect --help-agent` for command-specific context.
````

## Group: vm

````
# yeet vm Agent Context

## Purpose

Manage VM-specific commands

## Usage

```
yeet [GLOBAL_OPTIONS] vm COMMAND [ARGS...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Discovery

- Run `yeet vm console --help-agent` for command-specific context.
- Run `yeet vm images --help-agent` for command-specific context.
- Run `yeet vm kernel --help-agent` for command-specific context.
- Run `yeet vm memory --help-agent` for command-specific context.
- Run `yeet vm set --help-agent` for command-specific context.

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

Run `yeet vm console --help-agent` for command-specific context.

### `vm images`

Show available VM images and manage VM image cache state

Run `yeet vm images --help-agent` for command-specific context.

### `vm kernel`

Manage guest-selected VM kernels

Run `yeet vm kernel --help-agent` for command-specific context.

### `vm memory`

Show or set host VM memory policy

Run `yeet vm memory --help-agent` for command-specific context.

### `vm set`

Set VM resources and networking

Run `yeet vm set --help-agent` for command-specific context.
````

## Group Command: docker outdated

````
# yeet docker outdated Agent Context

## Purpose

Show Docker compose containers with upstream image updates

## Usage

```
yeet [GLOBAL_OPTIONS] docker outdated [SVC] [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: false

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
# yeet docker pull Agent Context

## Purpose

Pull images for a compose service without restarting

## Usage

```
yeet [GLOBAL_OPTIONS] docker pull <svc>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: docker push

````
# yeet docker push Agent Context

## Purpose

Push a local image into the internal registry

## Usage

```
yeet [GLOBAL_OPTIONS] docker push <svc> <image> [--run] [--all-local]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

### `IMAGE`

Local image ref

- **Type**: `string`
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

## Group Command: docker update

````
# yeet docker update Agent Context

## Purpose

Pull images and recreate containers for compose services

## Usage

```
yeet [GLOBAL_OPTIONS] docker update <svc...> | docker update --outdated
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICES`

Service names

- **Type**: `[]cli.ServiceName`
- **Required**: true
- **Variadic**: true (minimum: 1)

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
# yeet env copy Agent Context

## Purpose

Upload an env file

**Aliases**: `cp`

## Usage

```
yeet [GLOBAL_OPTIONS] env copy <svc> <file>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: env edit

````
# yeet env edit Agent Context

## Purpose

Edit the env file

## Usage

```
yeet [GLOBAL_OPTIONS] env edit <svc>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: env set

````
# yeet env set Agent Context

## Purpose

Set env keys

## Usage

```
yeet [GLOBAL_OPTIONS] env set <svc> KEY=VALUE [KEY=VALUE...]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: env show

````
# yeet env show Agent Context

## Purpose

Print the current env file

## Usage

```
yeet [GLOBAL_OPTIONS] env show <svc> [--staged]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: host set

````
# yeet host set Agent Context

## Purpose

Configure catch host storage

## Usage

```
yeet [GLOBAL_OPTIONS] host set [--data-dir=PATH_OR_DATASET] [--services-root=PATH_OR_DATASET_PREFIX] [--zfs] [--migrate-services=all|none] [--config=PATH] [--yes]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--data-dir`

Set catch data directory path or ZFS dataset

- **Type**: `string`

### `--services-root`

Set default root for service directories or ZFS dataset prefix

- **Type**: `string`

### `--zfs`

Treat supplied storage targets as ZFS datasets or dataset prefixes

- **Type**: `bool`

### `--migrate-services`

Service migration mode: all, none

- **Type**: `string`

### `--config`

Path to yeet.toml to update after service migration

- **Type**: `string`

### `--yes` (short: `-y`)

Confirm disruptive host storage changes without prompting

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

## Examples

```
yeet host set --data-dir=$HOME/yeet-data
```

```
yeet host set --services-root=$HOME/yeet-data/services2 --migrate-services=none
```

```
yeet host set --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services --migrate-services=all
```
````

## Group Command: service generations

````
# yeet service generations Agent Context

## Purpose

Show service generation rollback state

## Usage

```
yeet [GLOBAL_OPTIONS] service generations <svc> [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: true

## Options

### `--format`

Output format: table, json, json-pretty

- **Type**: `string`

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
# yeet service rollback Agent Context

## Purpose

Rollback a service to the previous generation

## Usage

```
yeet [GLOBAL_OPTIONS] service rollback <svc>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: service set

````
# yeet service set Agent Context

## Purpose

Set service settings

## Usage

```
yeet [GLOBAL_OPTIONS] service set <svc> [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit] [--snapshot-keep-last=N] [--snapshot-max-age=7d] [--snapshot-events=run,docker-update] [--snapshot-required=true|false]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet service sync Agent Context

## Purpose

Sync local yeet.toml service settings from catch

## Usage

```
yeet [GLOBAL_OPTIONS] service sync <svc> [--config=PATH] | service sync --all [--config=PATH]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `SERVICE`

Service name

- **Type**: `cli.ServiceName`
- **Required**: false

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
# yeet snapshots clone Agent Context

## Purpose

Clone a recovery point to a new service

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots clone <svc> <snapshot> <new-svc>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--start`

Reserved for future use; --start is currently unsupported for VM clones

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

## Examples

```
yeet snapshots clone <svc> yeet-20260613T203100Z-vm-manual-g0 <new-svc>
```
````

## Group Command: snapshots create

````
# yeet snapshots create Agent Context

## Purpose

Create a manual recovery point

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots create <svc> [--comment=TEXT] [--full]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--comment`

Human note stored with the recovery point

- **Type**: `string`

### `--full`

For VMs, also write Firecracker state and memory checkpoint files

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
# yeet snapshots defaults Agent Context

## Purpose

Show or set catch snapshot defaults

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots defaults show | snapshots defaults set [--enabled=true|false] [--keep-last=N] [--max-age=7d] [--events=run,docker-update] [--required=true|false]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet snapshots inspect Agent Context

## Purpose

Inspect one recovery point

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots inspect <svc> <snapshot> [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--format`

Output format: table, json, json-pretty

- **Type**: `string`

### `--output`

Alias for --format

- **Type**: `string`

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
# yeet snapshots list Agent Context

## Purpose

List yeet recovery points

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots list [svc] [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--format`

Output format: table, json, json-pretty

- **Type**: `string`

### `--output`

Alias for --format

- **Type**: `string`

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
# yeet snapshots protect Agent Context

## Purpose

Protect a recovery point from retention pruning

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots protect <svc> <snapshot>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet snapshots restore Agent Context

## Purpose

Restore disk state, service-root state, or full VM state from a recovery point

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots restore <svc> <snapshot> [--stop] [--start] [--yes] [--mode=disk|full] [--generation=current|snapshot]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--stop`

Stop the service before restoring

- **Type**: `bool`

### `--start`

Start the service after restoring

- **Type**: `bool`

### `--yes` (short: `-y`)

Skip the restore confirmation prompt

- **Type**: `bool`

### `--mode`

Restore mode: disk, full

- **Type**: `string`

### `--generation`

Service generation source: current, snapshot

- **Type**: `string`

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
# yeet snapshots rm Agent Context

## Purpose

Delete a yeet recovery point

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots rm <svc> <snapshot> [--yes]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--yes` (short: `-y`)

Skip the removal prompt

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

## Examples

```
yeet snapshots rm <svc> yeet-20260613T203100Z-vm-manual-g0
```
````

## Group Command: snapshots unprotect

````
# yeet snapshots unprotect Agent Context

## Purpose

Allow retention pruning for a recovery point

## Usage

```
yeet [GLOBAL_OPTIONS] snapshots unprotect <svc> <snapshot>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
# yeet vm console Agent Context

## Purpose

Stream VM serial console output

## Usage

```
yeet [GLOBAL_OPTIONS] vm console <svc>
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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

## Group Command: vm images

````
# yeet vm images Agent Context

## Purpose

Show available VM images and manage VM image cache state

## Usage

```
yeet [GLOBAL_OPTIONS] vm images [ls|catalog|update|import <name> <dir>|rm <name>|prune] [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Arguments

### `ACTION`

Action (update)

- **Type**: `string`
- **Required**: false

## Options

### `--allow-local-kernel`

Allow an imported VM image bundle to provide vmlinux

- **Type**: `bool`

### `--stdin`

Read an import bundle tar stream from stdin

- **Type**: `bool`

### `--yes` (short: `-y`)

Skip confirmation prompts

- **Type**: `bool`

### `--dry-run`

Show what would be pruned without removing anything

- **Type**: `bool`

### `--format`

Output format: table, json, json-pretty

- **Type**: `string`

### `--output`

Alias for --format

- **Type**: `string`

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

## Group Command: vm kernel

````
# yeet vm kernel Agent Context

## Purpose

Manage guest-selected VM kernels

## Usage

```
yeet [GLOBAL_OPTIONS] vm kernel sync <svc> [--restart]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--restart`

Restart the VM after syncing the selected kernel

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

## Examples

```
yeet vm kernel sync <svc>
```

```
yeet vm kernel sync <svc> --restart
```
````

## Group Command: vm memory

````
# yeet vm memory Agent Context

## Purpose

Show or set host VM memory policy

## Usage

```
yeet [GLOBAL_OPTIONS] vm memory [set --policy=safe|balanced|aggressive] [--format=table|json|json-pretty]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

## Options

### `--policy`

Host VM memory policy: safe, balanced, aggressive

- **Type**: `string`

### `--format`

Output format: table, json, json-pretty

- **Type**: `string`

### `--output`

Alias for --format

- **Type**: `string`

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
yeet vm memory
```

```
yeet vm memory set --policy=balanced
```

```
yeet vm memory --format=json
```
````

## Group Command: vm set

````
# yeet vm set Agent Context

## Purpose

Set VM resources and networking

## Usage

```
yeet [GLOBAL_OPTIONS] vm set <vm> [--vcpus=N] [--memory=SIZE] [--memory-min=SIZE] [--balloon=auto|off] [--disk=SIZE] [--net=svc|lan|svc,lan] [--macvlan-parent=IFACE] [--macvlan-vlan=ID] [--macvlan-mac=MAC]
```

## Operating Rules

- Prefer exact examples when they match the task.
- Use command-specific agent help before running an unfamiliar command.
- Do not invent flags; use only flags listed in this context or command help.
- Preserve arguments after `--` as payload or application arguments.

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
yeet vm set <vm> --vcpus=8 --memory=8g --disk=128g
```

```
yeet vm set <vm> --memory-min=1g --balloon=auto
```

```
yeet vm set <vm> --net=lan
```

```
yeet vm set <vm> --net=svc,lan --macvlan-parent=vmbr0 --macvlan-vlan=4
```
````
