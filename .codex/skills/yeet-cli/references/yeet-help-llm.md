# Yeet CLI --help-llm Outputs
Generated on 2026-05-15 from this repo using:
- `go run ./cmd/yeet --help-llm`
- `go run ./cmd/yeet <command> --help-llm`
- `go run ./cmd/yeet <group> --help-llm`

If command behavior changes, re-run the help commands and update this file.

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

Copy files between local and service data

**Aliases**: `cp`

**Examples**:

```
yeet copy ./config.yml svc:data/config.yml
```

```
yeet copy ./configs/ svc:data/
```

```
yeet copy svc:data/configs ./configs
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

Show detailed info about a service

Get detailed help: `yeet info --help-llm`

### `init`

Install catch on a remote host (local build or GitHub release)

**Examples**:

```
yeet init root@<host>
```

```
yeet init
```

Get detailed help: `yeet init --help-llm`

### `ip`

Show the IP addresses of a service

Get detailed help: `yeet ip --help-llm`

### `list-hosts`

List all hosts with the given tags

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

### `rollback`

Rollback a service

Get detailed help: `yeet rollback --help-llm`

### `run`

Install/update from a payload (binary, compose, image, Dockerfile)

**Examples**:

```
yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
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

Open SSH to the catch host (optionally into a service dir)

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

## Command: run

````
# yeet run

Install/update from a payload (binary, compose, image, Dockerfile)

## Usage

```
yeet [GLOBAL_OPTIONS] run [OPTIONS] SVC PAYLOAD [-- <payload args>]
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
yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
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

## Command: restart

````
# yeet restart

Restart a service

## Usage

```
yeet [GLOBAL_OPTIONS] restart [OPTIONS]
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
yeet [GLOBAL_OPTIONS] remove [OPTIONS]
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

## Command: init

````
# yeet init

Install catch on a remote host (local build or GitHub release)

## Usage

```
yeet [GLOBAL_OPTIONS] init [OPTIONS] [--from-github] [--nightly] [ROOT@HOST]
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
yeet init root@<host>
```

```
yeet init
```


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
