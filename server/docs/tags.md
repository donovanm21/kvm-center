---
title: VM tags
section: Virtual Machines
order: 35
---

# VM tags

Open a VM detail page on desktop or mobile and choose **Add tags** or **Edit
tags**. Enter comma-separated labels and save. A label is created when first
assigned; assign the same spelling to another VM to reuse it. Edit a VM's list
to rename or remove its assignments. Clearing the field removes all its tags.
There is no separate global tag definition or fleet-wide rename operation.

Tags use lowercase letters and digits, with dots, underscores and hyphens
allowed inside a name. Each tag is 1–32 characters; each edit accepts at most 24
entries before deduplication. Whitespace and case are normalized, duplicates
removed, and results sorted. Existing tags are shown while editing. Desktop and
mobile VM list filters also match tags.

Reading tag assignments and the catalog requires `vm.view`; editing requires
`vm.edit`. Scoped API tokens must include the corresponding permission as well
as having an owner permitted to perform the action. Read-only users see tags
without editing controls. Tag edits do not change the VM or send agent commands.

## API

`GET /api/vms/{uuid}/tags` returns `{"tags":["database","production"]}`.
VM list and detail responses also include a `tags` array. `GET /api/tags`
returns `{"tags":[{"tag":"database","count":2}]}`, sorted by name. Counts
include retained assignments whose VM is temporarily absent from inventory.

`PUT /api/vms/{uuid}/tags` replaces the complete assignment list conditionally:

```json
{"tags":["database"],"expected_tags":["database","production"]}
```

Both arrays are required, including explicit empty arrays for a new assignment
or clearing. Unknown fields, nulls, duplicate keys, trailing JSON and oversized
requests are rejected. The VM must exist in current inventory. The response is
200 with the normalized tags only after the checked save succeeds. Edits are
serialized and record the authenticated actor in the audit log.

- **400:** invalid tags or request; the edit is not staged.
- **401/403:** missing authentication or permission; no tag change.
- **404:** VM absent from current inventory.
- **409:** current tags differ from both the expected and desired lists. Reload
  tags, review the current assignment, then edit again. This is value comparison,
  not a monotonically increasing revision; a change away and back is equivalent
  to the original value.
- **503:** persistence unavailable or unconfirmed. An edit may already be staged
  or saved. Retry the identical request; do not assume rollback. A retry whose
  desired tags equal current tags performs another checked save. After another
  writer changes the list to a different value, an old retry conflicts.

The editor preserves a failed draft. **Reload tags** discards that draft and
loads current values for review. Cancel closes the editor; it cannot undo a
request whose persistence outcome was unconfirmed.

## Persistence and limits

Assignments belong to VM UUIDs, survive renames and moves, and are not pruned
when telemetry temporarily omits a VM. The versioned `vm_tag_state` format from
the persistence foundation covers JSON, Mongo and backup archives. A maximum of
4,096 tagged identities and 1 MiB compact tag state bounds storage. Older
binaries can drop or hide tag data; downgrade requires stopped writers and a
verified compatible pre-upgrade backup. Online restore remains disabled.

Tags currently organize and filter inventory. Alarm selectors and placement
rules are outside this feature.

## Validation boundaries

Browser workflows use the actual authenticated server and disposable Mongo for
desktop/mobile creation, reuse, editing, invalid input, conflicts, clearing,
search and page reload, plus a real read-only-user check. One separate mocked
503 browser test checks draft/error presentation only. Backend failure and retry
are tested in Go against a synthetic failing wrapper around JSON persistence.
Hosted Mongo tests exercise API writes and fresh Store reload after reconnect.
A browser reload alone does not prove process-restart durability. These tests
use synthetic inventory and do not validate native hypervisor operations.
