---
title: Resolving stranded commands
section: Operations
order: 65
---

# Resolving stranded commands

An `unknown` command means the server cannot establish whether the requested operation completed. Inspect the target VM and host before recording an observation. Resolving a command does not resend it, cancel an operation, or change the VM.

An authenticated operator with `user.manage` permission can send `POST /api/admin/command-log/resolve` with:

```json
{"request_id":"req_example","outcome":"success","note":"Confirmed the requested state on the target host"}
```

`outcome` must be `success` or `failed`; a nonempty observation note is required (maximum 4096 bytes). Only `unknown` entries can be resolved. The command retains the immutable actor, API token ID when applicable, note and resolution time. Existing command-log retention and capacity limits still apply after acknowledgement.

A `200` response confirms the store's checked persistence operation succeeded. A `503` means durability is unconfirmed: the observation remains protected in memory, and the same authenticated actor/token must retry the identical outcome and note. Do not assume a failed response rolled back the write. Identical retries also work after reload; conflicting changes return `409`. A missing or expired entry returns `404`.

Late reconciler results cannot overwrite a recorded resolution. The audit action is `command.resolved`. This endpoint uses the configured backend's existing persistence guarantees; it does not make multi-collection MongoDB writes transactional.
