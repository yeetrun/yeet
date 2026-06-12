# Web Run Workload Builder Design

## Goal

Redesign `yeet run --web` into a fast new-service creation workbench for homelab
users.

The web flow stays focused on first deploys. It does not become a lifecycle
console for existing services. It should make the common path quicker than
remembering every flag while preserving yeet's CLI model: the browser edits a
typed `RunDraft`, validation uses the existing backend validator, and deploy
executes the same draft path as CLI `yeet run`.

The default experience should optimize for:

- Compose app deploys as the most common workload.
- Keyboard-friendly setup with normal tab order and native form semantics.
- Network and ZFS choices that are easy to find because they are optional but
  common in homelab deployments.
- VM catalog deploys as a first-class workload without making the whole UI
  VM-centric.
- Cron jobs as a supported workload type, not a later "coming soon" placeholder.

## Product Scope

`yeet run --web` is for creating a new service from one of these workload types:

- Compose app.
- Virtual machine.
- Dockerfile.
- Container image.
- Binary or script.
- Scheduled job.

Existing service redeploys, service edits, VM resize flows, log viewers, and
runtime lifecycle actions remain out of scope. A successful deploy may show
next commands such as `yeet status`, `yeet logs`, `yeet ssh`, or `yeet console`,
but those are links back to normal yeet workflows, not embedded management
features.

Imported VM images, uploaded root filesystems, and local image injection are not
primary web-run flows. Users who need those paths can still use the CLI. The VM
web flow should be catalog-first, with manual `vm://...` entry available as an
advanced escape hatch.

## Recommended Shape

Use a single-screen workload builder.

The first visible control selects the workload type. The rest of the page stays
stable while the source section changes to match that workload. This keeps the
flow quick for repeat users, avoids a slow wizard, and works well with standard
keyboard navigation.

The page has five stable areas:

1. Target.
2. Source.
3. Deploy settings.
4. Advanced.
5. Review and deploy.

The UI should be compact, technical, and operational. It should not be a landing
page and should not include broad teaching copy. Field labels, short inline
help, validation messages, and docs links are enough.

## Screen Structure

### Target

The target area contains:

- Service name.
- Host selector or host input.
- Workload type selector.

The workload selector order should be:

1. Compose app.
2. Virtual machine.
3. Dockerfile.
4. Container image.
5. Binary/script.
6. Scheduled job.

Compose app should be selected by default unless CLI prefills or detected
payload context strongly indicate a different type.

### Source

The source area is the only primary section that changes by workload type. It
should use the existing cwd-rooted file API for local files instead of browser
filesystem APIs.

Compose app:

- File picker prioritizes `compose.yml`, `compose.yaml`, `docker-compose.yml`,
  and `docker-compose.yaml`.
- Optional env file.
- Optional pull behavior if that is valid for the selected payload path.
- Publish ports remain available when the deployment path supports them.

Virtual machine:

- Catalog image picker is the primary source control.
- Manual `vm://...` image reference is hidden under advanced source settings.
- CPU, memory, and disk fields are visible in the source section because they
  are core VM shape decisions.
- Default network is `svc`.

Dockerfile:

- File picker prioritizes `Dockerfile`.
- Build context can be inferred from the Dockerfile directory unless the current
  CLI contract requires explicit payload args.
- Publish ports are visible.

Container image:

- Image reference input.
- Pull toggle when applicable.
- Publish ports are visible.

Binary/script:

- File picker prioritizes executable-looking files.
- Payload args field.
- Optional env file.

Scheduled job:

- Schedule input accepts raw cron syntax.
- Lightweight presets can fill the raw cron field, but the raw expression stays
  visible.
- Source fields mirror the selected job payload shape where practical.

### Deploy Settings

Deploy settings are workload-independent controls that users commonly need:

- Network.
- Storage and ZFS.

These controls must stay visible without expanding an advanced section. They
are optional, but common enough that hiding them makes the web flow less useful
for the intended audience.

### Advanced

Advanced contains the lower-frequency fields:

- Tailscale version, exit node, tags, and auth key.
- macvlan parent, VLAN, and MAC.
- Snapshot policy.
- Manual VM image reference.
- Payload args when the selected workload normally does not need them.

Advanced sections should preserve values when collapsed. Collapsing is only a
presentation choice, not a reset.

### Review and Deploy

The review area contains:

- Generated command preview.
- Validation summary.
- Deploy button.
- Read-only terminal output after deploy starts.

The command preview should be generated from the same normalized draft that will
be sent to deploy. The preview is not the source of truth.

## Network Design

Network selection should be a compact multi-select control with plain labels
and flag-backed values.

For non-VM workloads:

- `host`: default host networking.
- `svc`: stable private service IP.
- `ts`: service tailnet IP.
- `lan`: LAN/macvlan attachment.

For VM workloads:

- `svc`: default managed network.
- `lan`: optional LAN presence.
- `ts`: hidden or disabled with an explicit validation message because VM
  Tailscale networking is not supported yet.
- `host`: hidden because it is not a VM network mode.

When a VM user selects `lan`, the UI should make `svc,lan` easy to choose and
explain in field help that `svc,lan` gives both LAN presence and the reliable
yeet-managed SSH/proxy path. LAN-only remains possible for users who know they
want it.

Tailscale-specific fields should appear when `ts` is selected for non-VM
workloads. macvlan fields should appear when `lan` is selected. If a user enters
macvlan details without selecting `lan`, validation should point them to
`--net=lan` or `--net=svc,lan`.

Publish ports stay near network settings for payloads that support them.

## Storage and ZFS Design

Storage should be a visible deploy setting for every workload:

- Service root is optional.
- Without ZFS, service root is an absolute filesystem path on the catch host.
- With ZFS enabled, service root is a ZFS dataset name.

The ZFS toggle should change the service-root label, placeholder, and help text
instead of adding a separate field. Suggested placeholders should adapt by
workload:

- Compose, Dockerfile, container image, binary/script, and cron:
  `tank/apps/<service>`.
- VM: `tank/vms/<service>`.

Snapshot policy belongs in advanced storage and should only become prominent
after ZFS is enabled. The default snapshot behavior should come from the
existing backend policy; the web UI should not invent its own defaults.

## Data Flow

Keep the existing web-run API model:

- Browser holds a `RunDraft`.
- `/api/bootstrap` returns cwd, host/default context, and option metadata.
- `/api/files` powers local file selection.
- `/api/validate` validates and normalizes the draft.
- `/api/deploy` starts deploy after re-validating the draft.
- Deploy output streams through the existing deploy job stream endpoint.

The redesign is mostly client-side presentation plus draft construction. It
should not introduce a second command grammar in JavaScript.

Workload selection maps to draft fields:

- Workload source controls set `payload`, `payloadKind`, and workload-specific
  fields such as VM sizing or payload args.
- Network controls set `network.modes`, Tailscale fields, macvlan fields, and
  published ports.
- Storage controls set `storage.serviceRoot`, `storage.zfs`, and snapshot
  fields.

Validation results should be attached to the exact form field when possible and
also summarized near the deploy button.

## Error Handling

Validation errors should be actionable and field-specific.

Important cases:

- VM `ts` networking is unsupported; supported VM modes are `svc`, `lan`, or
  both.
- macvlan details require LAN networking.
- ZFS service root expects a dataset name, not an absolute path.
- Non-ZFS service root expects an absolute path.
- Cron schedule parsing errors should point at the schedule field.
- Missing source files or path traversal errors should point at the source
  field or file picker.

Deploy errors remain visible in the terminal output. The page should preserve
the draft after failure and make retrying obvious. A successful deploy should
lock the session into a success state, show concise next commands, and avoid
pretending to be a full service console.

## Accessibility and Keyboard Behavior

Use native HTML controls where possible:

- Normal tab order from target, to source, to deploy settings, to review.
- Workload selector implemented with radio buttons, tabs with correct roles, or
  another standard keyboard-accessible control.
- No custom keyboard traps.
- Enter and space follow native control behavior.
- Validation messages are associated with their fields.
- Deploy status changes are announced through accessible status regions.

The page should work efficiently without a mouse. Keyboard use is a core
requirement, not a polish item.

## Implementation Boundaries

The implementation should keep these boundaries clear:

- Go owns draft validation, normalization, command preview, and deploy.
- JavaScript owns interaction state and form-to-draft mapping.
- The generated command preview is display output from the backend-normalized
  draft, not a hand-built command string used for deploy.
- Workload-specific source mapping should be isolated from generic network,
  storage, validation, and deploy-output UI.

If the current browser asset file becomes too large to understand safely, split
it by responsibility while preserving the embedded static asset approach.

## Testing

Add focused tests for:

- Workload-to-draft mapping for each workload type.
- Compose being the default workload.
- VM catalog source generating VM payload draft fields.
- Manual VM source staying advanced but still producing the same draft shape.
- Network defaults for regular services and VMs.
- VM rejection or disabling of `ts`.
- `lan` showing and validating macvlan fields.
- ZFS toggle changing service-root interpretation.
- Snapshot controls writing only existing snapshot draft fields.
- Generated command preview using the normalized backend draft.
- Existing `/api/validate`, `/api/deploy`, and deploy stream contracts staying
  compatible.

Manual verification should include:

- Keyboard-only run through a Compose deploy draft.
- Keyboard-only run through a VM catalog deploy draft.
- Network selection for `svc`, `lan`, `svc,lan`, and `ts` where supported.
- ZFS dataset entry for app and VM placeholders.
- Failed validation preserves values and points at the right fields.
- Failed deploy preserves values and allows retry.

## Documentation

Update the user manual when the implementation lands:

- `yeet run --web` is a new-service creation workbench.
- Workload types it supports.
- Network choices are available in the web UI.
- ZFS-backed service roots are available in the web UI.
- VM flow is catalog-first.
- Existing service edits still use CLI commands.

The docs should stay user-facing. They should not describe internal `RunDraft`
or API mechanics unless troubleshooting requires it.
