# Service Publish Ports Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Add persistent published-port settings through `yeet run` and `yeet service set`.

**Architecture:** Treat `ports = [...]` as the canonical client config field and `db.Service.Publish` as the catch-side desired state. `yeet run` and `yeet service set` both feed the same normalization and missing-port guard, then catch applies the complete desired list to durable compose artifacts before recreating the compose service.

**Tech Stack:** Go CLI parser, yargs flag metadata, catch JSON DB/view generation, Docker Compose YAML helpers, website MDX docs.

---

### Task 1: Parser And Config Model

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/yeet/project_config.go`
- Test: `pkg/cli/cli_test.go`
- Test: `pkg/yeet/project_config_test.go`

- [x] Add failing parser tests for `service set -p 80:80`, repeated `-p`, and `--publish-reset`.
- [x] Add failing config tests for `ServiceEntry.Ports` cloning, TOML round trip, and `saveRunConfig` migrating `--publish` args into `ports`.
- [x] Add `Publish []string` and `PublishReset bool` to run/service-set flags.
- [x] Add `Ports []string` to `ServiceEntry`, preserve it in `ServiceEntry`, `SetServiceEntry`, and save/load paths.
- [x] Add helpers that extract publish flags from run args, normalize the stored list, remove publish flags from `Args`, and emit run args from stored ports.
- [x] Run `go test ./pkg/cli ./pkg/yeet -run 'Test(ParseServiceSet|ProjectConfig|SaveRunConfig|RunFromProjectConfig)' -count=1`.

### Task 2: Client Safety Semantics

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Test: `pkg/yeet/svc_cmd_branch_test.go`
- Test: `pkg/yeet/handle_svc_cmd_config_test.go`

- [x] Add failing tests showing `yeet service set nginx -p 443:443` errors when stored `80:80` would be omitted.
- [x] Add failing tests showing full desired list succeeds, `--publish-reset -p 443:443` replaces, and `--publish-reset` clears.
- [x] Add failing tests showing `yeet run` preserves stored ports when no `-p` is supplied and applies the same guard when explicit `-p` omits a stored port.
- [x] Implement a shared missing-port guard with Tailscale-style messaging and suggested rerun commands.
- [x] Update local `yeet.toml` only after remote success; return `updated catch service settings, but failed to update yeet.toml: <error>` on split success.
- [x] Run `go test ./pkg/yeet -run 'Test(ServiceSet|RunFromProjectConfig|HandleSvcCmdRun)' -count=1`.

### Task 3: Catch Persistent Port State

**Files:**
- Modify: `pkg/db/db.go`
- Generate: `pkg/db/db_view.go`
- Generate: `pkg/db/db_clone.go`
- Modify: `pkg/catch/compose_ports.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/tty_service_set.go`
- Test: `pkg/db/db_test.go`
- Test: `pkg/catch/compose_ports_test.go`
- Test: `pkg/catch/tty_service_set_test.go`
- Test: `pkg/catch/installer_file_test.go`

- [x] Add failing DB tests proving `Publish` is cloned/viewed.
- [x] Add failing compose tests for reading ports, clearing ports, and normalizing `tcp`.
- [x] Add failing catch `service set` tests for replacing compose `ports`, recording `Publish`, and rejecting missing existing mappings without reset.
- [x] Add `Publish []string` to `db.Service`, bump data version if needed, and regenerate DB views/clones with `go generate ./pkg/db`.
- [x] Record normalized publish lists during install/redeploy.
- [x] Implement catch-side `service set -p/--publish-reset`: validate compose-backed service, derive current ports from DB or compose fallback, write a new durable compose generation, update DB, and run compose `up -d`.
- [x] Run `go test ./pkg/db ./pkg/catch -run 'Test.*(Publish|ComposePorts|ServiceSet)' -count=1`.

### Task 4: Info And Sync

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/service_sync.go`
- Test: `pkg/catchrpc/types_test.go`
- Test: `pkg/catch/service_info_test.go`
- Test: `pkg/yeet/info_cmd_test.go`
- Test: `pkg/yeet/service_sync_test.go`

- [x] Add failing RPC/info tests for structured `network.ports` and plain `yeet info` port rows.
- [x] Add failing sync tests for writing local `ports` from catch and preserving local ports when an older catch omits port data.
- [x] Add `ServicePort` and `ServiceNetwork.Ports`.
- [x] Populate service info from `db.Service.Publish` or compose fallback.
- [x] Render compact plain output and include JSON via existing response structs.
- [x] Sync catch port data into `ServiceEntry.Ports`.
- [x] Run `go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -run 'Test.*(ServiceInfo|Info|ServiceSync)' -count=1`.

### Task 5: Docs And Verification

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/concepts/service-types.mdx`
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-llm.md`

- [x] Update help text for `service set` and `run`.
- [x] Update README and website docs for `-p/--publish`, `--publish-reset`, missing-port guard, `yeet info`, and compose compatibility.
- [x] Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catchrpc ./pkg/catch ./pkg/db ./pkg/svc -count=1`.
- [x] Run `go test ./... -count=1`.
- [x] Run `git -C website diff --check`.
- [x] Run `rg -n "private-host|/Users/" README.md website/docs .codex/skills`.
