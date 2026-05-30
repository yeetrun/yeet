# Web Run Design

## Goal

Add a local browser-based frontend for first-time `yeet run` deployments:

```bash
yeet run --web
yeet run --web <svc>
yeet run --web <svc> <payload>
```

The command starts a localhost-only web server from the current `yeet` process,
opens the user's browser, and serves an embedded HTML/CSS/JS deployment form.
The user configures a new service in the browser, clicks deploy, then closes the
tab and returns to the terminal. The terminal remains the owner of the session
and streams the authoritative deploy progress.

The web flow is a frontend for the same deployment behavior as CLI `yeet run`.
On success it writes `yeet.toml` exactly as a normal CLI deployment would.

## Scope

V1 is for new services only. Before deploy, yeet checks the selected catch host
and rejects the submission if the service already exists. The error should say
that web deploy currently supports new services only and that existing services
should be redeployed from the CLI for now.

The design must not block future redeploy or reconfiguration flows. Existing
service support should be a validation mode and UI mode layered on the same
typed draft model, not a separate implementation.

V1 exposes first-deploy options:

- service name
- host
- payload path, Dockerfile, compose file, local image, or remote image ref
- payload args
- env file
- network mode settings
- Tailscale version, exit node, tags, and auth key
- publish ports where valid
- service root and ZFS dataset mode
- snapshot overrides
- macvlan fields

V1 does not expose redeploy-oriented flags such as `--pull` and `--force`.
Implementation should leave an explicit code comment near the form schema or
draft option mapping noting that these flags belong to future existing-service
redeploy support.

## Shared Run Draft

Introduce a typed `RunDraft` as the shared deploy intent for CLI and web. CLI
args parse into a draft. The web form posts a draft as JSON. A shared validator
normalizes the draft and a shared executor deploys it.

Conceptual fields:

```go
type RunDraft struct {
	Service string
	Host    string

	Payload     string
	PayloadKind string // auto, file, compose, dockerfile, local-image, remote-image
	EnvFile     string
	PayloadArgs []string

	Network RunDraftNetwork
	Storage RunDraftStorage
	Snapshots RunDraftSnapshots

	NewServiceOnly bool
}
```

The exact Go shape can be refined during implementation, but the boundary must
stay typed. JavaScript should not construct raw CLI strings as the source of
truth, and the backend should not treat web deploy as a separate command path.

CLI `yeet run` should keep its current behavior while changing internally to:

1. Parse command-line args into `RunDraft`.
2. Validate and normalize the draft.
3. Execute the draft through the shared deploy executor.
4. Persist project config through the existing `yeet.toml` writer.

The web path should follow the same steps, with JSON replacing command-line
parsing.

## Web Command Lifecycle

`yeet run --web [svc] [payload]` should:

1. Resolve cwd and load `yeet.toml` if present.
2. Resolve host candidates from `yeet.toml`, `CATCH_HOST`, and the default
   prefs host.
3. Apply optional CLI prefills for service and payload.
4. Start a local HTTP server on `127.0.0.1` with an automatically selected high
   port.
5. Generate a random per-session token.
6. Open the browser to the local URL containing that token.
7. Keep the terminal process running until deploy completes, the server is
   interrupted, or the user exits.

After successful deploy, the browser should show a concise success state and
tell the user to close the tab and return to the terminal. The terminal should
print the final service status or normal deploy completion output.

If the browser is closed during deployment, the deployment continues. If the
terminal process is interrupted, yeet cancels active validation/deploy work and
shuts down the web server.

## HTTP API

Serve all static assets from `go:embed`. Use a small number of JSON endpoints:

- `GET /api/bootstrap`
  - Returns cwd display path, project config path, host candidates, selected
    host, CLI prefills, supported option metadata, and validation defaults.
- `GET /api/files?dir=<relative-dir>`
  - Returns cwd-rooted directory entries for the file picker.
- `POST /api/validate`
  - Accepts a `RunDraft`, validates it, and returns normalized field values,
    warnings, and inline errors.
- `POST /api/deploy`
  - Repeats validation, enforces new-service-only mode, then executes the
    normalized draft.

All JSON endpoints require the session token. Static assets may be served only
from the generated session URL. Bind only to `127.0.0.1`; do not listen on all
interfaces.

The browser shows coarse deployment states: `ready`, `validating`, `deploying`,
`done`, and `failed`. Detailed logs stay in the terminal.

## Host Selection

The host selector should be populated from:

- hosts listed in `yeet.toml`
- hosts used by existing `yeet.toml` service entries
- `CATCH_HOST`, when set
- the default prefs host

The initially selected host should match yeet's normal host resolution. Manual
host entry is allowed but secondary because the common path is already-known
hosts.

The UI should asynchronously verify the selected host before submit. Host
verification should call catch info/status through the same client stack used by
the CLI. A bad host should produce an inline error and disable deploy until
resolved.

## File Selection

Do not depend on browser file system APIs. They are not portable enough and are
less appropriate than a local yeet-owned file API.

The file picker is served by yeet and rooted at the directory where
`yeet run --web` was launched:

- `GET /api/files?dir=.` lists entries under cwd.
- Relative paths are interpreted relative to cwd.
- The backend rejects path traversal outside cwd.
- Symlinks that escape cwd are rejected.
- Manual entry may use relative paths, absolute paths, or image references.
- The tree/list should highlight likely payloads such as `compose.yml`,
  `docker-compose.yml`, `Dockerfile`, executable files, and common env files.

Choosing a file fills the path field. The draft still stores a path string, not
a browser file handle.

## UI Design

The UI should be desktop-first, compact, and technical. It only needs to be
responsive enough not to break on narrower windows; it does not need a dedicated
mobile experience.

Use the website's visual language:

- dark gray base
- green brand accent
- teal secondary accent
- restrained borders
- 8px-ish radii
- yeet mark/logo

The first screen is the deployment form, not a landing page.

Suggested structure:

- Top bar: yeet mark, project directory, selected host verification state.
- Main form: desktop-oriented two-column layout.
- Left column: service, host, payload browser, env file, payload args.
- Right column: network, storage, snapshots, macvlan/advanced settings.
- Bottom sticky action row: generated command preview, validation state, deploy
  button.

Controls should use standard HTML semantics wherever possible:

- first field focused on load unless service is prefilled
- normal tab navigation
- enter/space behavior on native controls
- no custom keyboard traps
- repeatable rows for tags, ports, and payload args
- small `?` tooltip buttons beside non-obvious fields

Tooltip help keeps the UI minimally labeled while documenting details such as
network modes, Tailscale tags/auth key, service root vs ZFS dataset, snapshots,
macvlan, publish ports, and payload args.

## Validation

Validation runs on blur where useful, on explicit validate, and again before
deploy. The deploy endpoint must not trust earlier browser validation.

Backend validation should cover:

- service name is required and syntactically valid
- host is reachable and catch responds
- selected service does not already exist when `NewServiceOnly` is true
- payload is present
- file payloads exist when required
- env file exists when provided
- Dockerfile and compose payloads are detected consistently with CLI behavior
- remote image refs are accepted when they match current image-ref heuristics
- non-ZFS service roots are absolute paths
- ZFS service roots are dataset names and require ZFS mode
- network-specific fields apply only when their mode is selected
- snapshot mode and retention fields parse correctly
- compose publish-port restrictions match current CLI behavior

Inline errors should be field-specific where possible. Deploy failures should
show a short browser summary and direct the user to the terminal output for the
full error.

## Persistence

Successful web deploy writes the same local config as CLI `yeet run`:

- service name
- host
- payload path relative to `yeet.toml` directory when current behavior does
  that
- env file path when provided
- service root and ZFS flag
- snapshot overrides
- run args and payload args in the same normalized representation

There should be no web-specific config file and no alternate persistence path.
Catch remains the source of truth for live service state. `yeet.toml` remains
the local replay recipe.

## Safety

The web server is local and short-lived:

- bind to `127.0.0.1`
- use an automatically selected high port
- require a per-session token for JSON requests
- reject path traversal and cwd-escaping symlinks
- shut down on terminal interrupt
- close or become inert after deploy completion

The browser should never receive secret values from existing configs unless the
user entered them in this session. If an auth key or similar secret is accepted
by the form, treat it as write-only UI state and avoid echoing it in command
preview output.

## Website And Documentation

After implementation, capture real screenshots of the local web deploy UI.
Update the website homepage so the web deploy path appears prominently next to
the CLI deploy example. The goal is that a new visitor quickly understands both
ways to deploy a payload.

Docs to update:

- CLI manual for `yeet run --web`
- getting-started or homepage deploy example with screenshot
- configuration docs noting that web deploy writes the same `yeet.toml` entries
  as CLI deploy

Screenshots should use the real implemented UI, not mockups.

## Tests

Add focused tests around the shared draft boundary and local web behavior:

- CLI args parse into the expected `RunDraft`.
- Web JSON drafts validate into the same normalized draft.
- Draft execution preserves current `yeet run` behavior.
- New-service-only validation rejects existing services.
- Host bootstrap includes `yeet.toml`, `CATCH_HOST`, and default prefs hosts.
- Host verification reports bad hosts before deploy.
- Cwd file API rejects traversal and symlink escapes.
- Deploy API repeats validation before executing.
- `yeet.toml` saved by web deploy matches normal CLI deploy behavior.
- Embedded assets serve correctly.
- Form schema includes all first-deploy supported flags.
- Redeploy-only flags such as `--pull` and `--force` are not exposed in v1.

Live validation should deploy a small new service from a fixture project to a
real catch host, then verify:

- browser reaches a success state
- terminal reports successful deployment
- `yeet status <svc>` is correct
- `yeet.toml` contains the expected service entry

## Out Of Scope

- Existing-service redeploys from the web UI.
- Service reconfiguration from the web UI.
- Rollback or snapshot browsing.
- A full local file manager outside the launch cwd.
- Browser File System Access API integration.
- Mobile-specific UI work.
- WebSocket or browser-side detailed log streaming.
