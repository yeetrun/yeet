# VM Runtime Status Human Output Design

## Goal

Make `yeet vm runtime status` useful in an ordinary terminal.

The current human output prints thirteen columns containing complete artifact
IDs and SHA-256 digests. That is exact, but it is not a table in any practical
sense. `text/tabwriter` expands every column to the longest value, the terminal
wraps the resulting line wherever it happens to run out of space, and the
headers become detached from the values they describe.

Human output should answer the operational questions first:

- What runtime is running now?
- What runtime will an ordinary start use?
- Is another runtime staged?
- Is the VM healthy, and does it need operator attention?

Exact identities remain available in JSON. Humans get the useful shape of the
state; machines keep every byte of it.

## Rendering Model

Use two deterministic human renderers rather than trying to make one enormous
table adapt to every job.

The renderer choice follows command intent, not row count. An explicitly
selected VM gets the detail renderer. An unfiltered fleet command gets the
overview renderer even when the host happens to contain only one adopted VM.
The selected/unfiltered context therefore needs to reach the rendering helper;
`len(rows) == 1` is not enough information.

### One selected VM

`yeet vm runtime status <vm>` renders a vertical detail block:

```text
devbox  healthy

  Guest base:  ubuntu-26.04-amd64-v11 (custom legacy)
  Kernel:      linux-7.1.2-yeet (custom legacy)

  Runtime
    Running:     v1.16.1 / yeet-v1 [29d83ce729b5] (official, supported)
    Configured:  same as running
    Staged:      -
    Previous:    v1.14.3 [9b8e6f70ae9c] (custom legacy)

  Policy:      manual / stable
  Promoted:    v1.16.1 / yeet-v1 (official, supported)
  Isolation:   jailer
  Last change: 2026-07-22 15:40:39 UTC
```

If the row has a recommended action, render it as a final indented sentence:

```text
  Action: Restart when downtime is acceptable to establish a trusted runtime marker.
```

The detail block includes guest-base, kernel, previous-runtime, isolation, and
transition information because the user asked about one VM. Labels stay next
to their values even when a terminal wraps an unusually long fallback value.

### Fleet overview

`yeet vm runtime status` renders a compact table:

```text
VM       RUNNING          CONFIGURED       STAGED  POLICY         STATE
devbox   unverified       1.14.3 legacy    -       manual/stable  marker unverified
hermes   1.16.1 official  1.16.1 official  -       manual/stable  healthy
worker   1.16.1 official  1.16.1 official  -       manual/stable  healthy

Promoted stable runtime: 1.16.1 / yeet-v1

Needs attention:
  devbox: Restart when downtime is acceptable to establish a trusted runtime marker.
```

The fleet table omits guest-base, kernel, previous-runtime, isolation, digests,
and the action column. Those fields are useful in a detail view but turn a
fleet scan back into the same horizontal accident. Recommended actions appear
below the table only for rows that have one.

If every row has the same promoted channel target, render one promoted-runtime
summary below the table. If promoted targets differ, are unavailable, or come
from different effective channels, omit the shared summary rather than imply a
false fleet-wide value. Per-VM promoted identities remain visible in the detail
and JSON formats.

Recommended actions use a hanging indent and deterministic word wrapping so a
long action does not recreate the overflow below the table.

## Human Identity Formatting

Human formatting must preserve meaning without reproducing storage keys.

- Prefer a runtime's upstream version and Yeet release suffix when available.
- Label provenance as `official`, `legacy`, or `local` using the stored source.
- Include support state when it changes an operator decision, such as
  `supported`, `eol`, or `revoked`.
- Show a twelve-character manifest fingerprint in the single-VM detail when a
  manifest digest exists. This is enough to compare visible selections without
  pretending a shortened value is the full identity.
- Remove content-address suffixes from component labels. Keep the recognizable
  guest release, kernel release, or artifact name plus its provenance.
- Render a missing live marker as `unverified`, not as the configured runtime.
  Configured state is not proof of the process that is running.
- Render absent optional values as `-`.
- Fall back to a trimmed artifact ID when an identity does not match a known
  official or legacy shape. Never discard the value merely because it is odd.

The renderer must not infer provenance from a friendly version string. Source,
support, state, and lifecycle relationships still come from the authoritative
status row.

## JSON Contract

`--format=json` and `--format=json-pretty` keep the same schema and exact field
values. They continue to include complete IDs, manifest SHA-256 values,
component digests, paths, sources, support state, and lifecycle fields.

This change affects only `table`, including the default format. Scripts should
already use JSON; changing the human renderer must not create a second machine
format by accident.

## Width and Output Rules

Do not add terminal-width detection. Catch renders this output remotely and
the command can pass through RPC, SSH, pipes, logs, or redirected files. Width
at one hop is not a reliable description of width at the next.

Instead, bound the data placed in the fleet columns and use vertical
label/value lines for detail. The fleet fixture should remain readable within
100 columns for ordinary service names and supported runtime versions. A long
service name may still wrap; the rest of the table must not become hundreds of
columns wide because one identity contains a digest.

Output ordering remains deterministic. Fleet rows stay sorted by service name,
matching the current status-row ordering.

## Error Handling

Unsupported format errors remain unchanged. JSON encoding errors and writer
errors continue to propagate.

Human formatting helpers must be total over valid status rows. Unknown source
strings, missing catalog data, missing live markers, and unusual artifact IDs
use explicit fallback labels instead of failing the status command. Status is
an inspection path; formatting should not hide the underlying state because a
new identity shape arrived first.

## Testing

Add focused renderer tests that prove:

- one selected row uses the vertical detail layout;
- several rows use the compact fleet layout;
- complete SHA-256 values do not leak back into human output;
- a twelve-character fingerprint appears in detail output when available;
- long legacy IDs produce meaningful bounded labels;
- unverified running state is not replaced with configured state;
- actions render below the fleet table rather than as a wide column;
- the shared promoted-runtime summary appears only when it is actually shared;
- absent values and unknown identity shapes have deterministic fallbacks;
- representative fleet lines stay within the agreed width bound; and
- JSON output retains exact IDs and full digests.

Run the focused `pkg/catch` renderer tests, the complete `pkg/catch` suite, the
full Go suite, and the repository pre-commit gate before integration.

## Scope

This is a rendering change only. It does not alter CLI syntax, permissions,
catalog selection, runtime staging, restart behavior, marker trust, rollback,
or the JSON status schema. The command remains a read-permission operation.
