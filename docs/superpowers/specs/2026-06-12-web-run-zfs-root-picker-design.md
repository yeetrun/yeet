# Web Run ZFS Root Picker Design

## Goal

Make ZFS-backed service roots easier to choose in `yeet run --web` without
turning the web flow into a raw ZFS browser.

When a user enables ZFS, the service-root field should offer smart root
suggestions from the selected catch host. A user can pick a parent dataset such
as `flash/yeet/vms`, and yeet fills the final service dataset such as
`flash/yeet/vms/adguard-home`. The field remains editable so users can type any
valid dataset manually.

The feature is generic across payload types, but it should rank suggestions by
workload. VM payloads should prefer VM service roots. Compose, Dockerfile,
container image, and binary/script payloads should prefer normal service roots.

## Non-Goals

- Do not require host-level configuration for the first version.
- Do not add a full tree browser for every ZFS dataset.
- Do not create parent datasets from the picker.
- Do not change the existing `--service-root=<dataset> --zfs` deploy contract.
- Do not expose VM image cache datasets as recommended service destinations.

## User Experience

When `ZFS dataset` is enabled, the service-root control becomes a dataset input
with a `Pick` button. The input still accepts free text.

The picker shows a short ranked list for the selected host:

```text
Recommended
flash/yeet/vms       VM services root | 4 VMs | 1.33T free
flash/yeet           Services root | 24 services | 1.33T free

Other dataset roots
rust                 Pool root | 7 children | 6.83T free
flash                Pool root | 4 children | 1.33T free
```

Selecting a candidate fills the final dataset using the current service name:

```text
flash/yeet/vms/adguard-home
```

If the service name is blank, selecting a candidate fills a prefix with a
trailing slash and keeps focus in the field:

```text
flash/yeet/vms/
```

If the service name changes after a user selected a suggested root, the UI may
update the suffix only while the field still matches the picker-generated
template. Manual edits must be preserved.

The picker is hidden for workload paths where web deploy does not support
service roots.

## Host Capability States

The UI should distinguish these cases:

- `available`: ZFS discovery succeeded and candidates may be shown.
- `host-unreachable`: the selected host cannot be reached.
- `unsupported-rpc`: catch is reachable but does not support the discovery RPC.
- `zfs-missing`: the host does not have the `zfs` command or ZFS support.
- `no-filesystems`: ZFS exists but no usable filesystem datasets were found.
- `error`: discovery failed for another reason.

All non-available states keep manual entry available. The picker should show a
short message rather than blocking deploy:

```text
No ZFS datasets found on this host.
```

Validation and deployment remain authoritative. If the final dataset is invalid
or cannot be created, the normal validation/deploy error should explain why.

## API Shape

Add a catch RPC:

```text
catch.ZFSServiceRootCandidates
```

Request:

```json
{
  "workload": "vm",
  "service": "adguard-home"
}
```

Response:

```json
{
  "state": "available",
  "candidates": [
    {
      "dataset": "flash/yeet/vms",
      "mountpoint": "/flash/yeet/vms",
      "freeBytes": 1460000000000,
      "childCount": 4,
      "vmChildCount": 4,
      "serviceChildCount": 0,
      "suggestedDataset": "flash/yeet/vms/adguard-home",
      "label": "VM services root",
      "rank": 100
    }
  ],
  "warnings": []
}
```

Add a local web API route:

```text
GET /api/zfs-roots?host=yeet-pve1&workload=vm&service=adguard-home
```

The local web server calls the selected host's catch RPC and returns the typed
response to the browser. If the RPC is missing, the route returns
`state: "unsupported-rpc"` instead of treating it as a hard error.

## Candidate Discovery

Catch is the source of truth for host ZFS state. It should inspect datasets with
argument-based process execution, not shell strings:

```bash
zfs list -H -p -o name,type,mountpoint,available,used,refer,origin,canmount,readonly -t filesystem,volume
```

The discovery code builds a lightweight dataset tree.

Candidate roots are filesystem datasets that are mounted normally and are not
read-only. Volume datasets are never root candidates.

For each filesystem dataset, count:

- direct child filesystem datasets
- direct child volume datasets
- direct children whose own `root` child is a volume
- direct children that look like normal service datasets

A VM-shaped child is a filesystem dataset with a direct `root` volume child.
For example:

```text
flash/yeet/vms/adguard-home
flash/yeet/vms/adguard-home/root
```

This makes `flash/yeet/vms` a strong VM root candidate.

## Ranking

Ranking is workload-aware.

For VM payloads:

- Prefer roots with several VM-shaped children.
- Prefer names ending in `/vms`.
- Prefer roots under a broader yeet-managed namespace such as `*/yeet/vms`.
- Demote generic pool roots unless there are no better options.

For compose, Dockerfile, container image, and binary/script payloads:

- Prefer roots with many direct service-like filesystem children.
- Prefer names such as `*/yeet`, `*/apps`, or `*/services`.
- Demote roots where most children are VM-shaped.

For auto workload:

- Prefer general service roots, but still include VM roots lower in the list.

Internal datasets should be excluded or heavily demoted:

- `*/vm-images`
- image-version children under `*/vm-images`
- datasets whose primary purpose appears to be base image storage

The response should be capped to a small list, around 8 to 12 candidates.

## Data Flow

1. Browser loads bootstrap data as today.
2. User selects a host, workload, service name, and enables ZFS.
3. Browser requests `/api/zfs-roots` for the selected host/workload/service.
4. Local web server calls `catch.ZFSServiceRootCandidates`.
5. Browser renders ranked candidates.
6. User selects a candidate.
7. Browser writes `candidate.suggestedDataset` into `serviceRoot`.
8. Existing draft validation and command preview include
   `--service-root=<dataset> --zfs`.

The command preview remains derived from the draft. The picker is only an input
helper.

## Error Handling

Discovery errors should not prevent a deploy attempt. The final draft
validation and catch install path still enforce correctness.

Useful user-facing picker messages:

```text
Could not reach yeet-pve1.
This catch version does not support ZFS root discovery.
ZFS is not installed on this host.
No ZFS filesystem datasets were found on this host.
Could not load ZFS roots: <short error>
```

The local API should avoid leaking long command output into the browser. Catch
logs can keep detailed stderr for debugging.

## Testing

Add focused tests for:

- Catch parser and tree builder for `zfs list` rows.
- Capability detection for missing `zfs`, no filesystems, and command errors.
- VM-shaped root detection.
- Service-shaped root detection.
- Excluding or demoting `*/vm-images`.
- Workload-aware ranking.
- Suggested dataset construction with and without a service name.
- Catch RPC request/response types.
- Local `/api/zfs-roots` behavior for success, host selection, unsupported RPC,
  and discovery failure.
- Web assets: picker is shown only when ZFS is enabled, candidate selection
  fills the field, and manual edits are preserved.

After implementation, run one live check against `yeet-pve1`:

- VM workload should rank `flash/yeet/vms` above `flash/yeet`.
- Compose or container workload should rank `flash/yeet` above
  `flash/yeet/vms`.
- Manual entry should still work when discovery is unavailable.
