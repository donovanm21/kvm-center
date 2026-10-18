# Retained passthrough inventory evidence

`GET /api/vms/{uuid}/passthrough-evidence` requires `vm.view` and returns a detached projection of persisted inventory. It reports `source: persisted_inventory`, `status: unverified_inventory` and `native_verified: false`. It does not establish current attachment, device identity inside a guest, IOMMU isolation, or permission to attach, detach, migrate or grow anything.

The legacy BSON field `haspassthrough` remains outside the writable VM model. Current agent telemetry that omits it cannot silently replace a retained true value with false. The endpoint reads authoritative Mongo BSON or a retained JSON overlay under the persistence consistency locks. Pending in-memory edits are not asserted as durable evidence.

A missing flag is returned as null (unknown), distinct from explicit false. Only the exact boolean field is supported. Conflicting aliases, duplicate fields, ambiguous VM identity, malformed or oversized PCI records, duplicate PCI addresses and a false flag with nonempty PCI inventory are refused. A true flag with no PCI records may describe non-PCI passthrough and is not inferred to be contradictory. Missing, null and present PCI arrays have distinct statuses. PCI records require exact supported source-address and description fields. Unknown nested PCI semantics are preserved by existing storage guards but refused by this view; unrelated opaque fields are neither exposed nor modified.

Supported typed PCI inventory edits retain the existing persistence contract. Containers with unknown descendants cannot be edited lossily. The endpoint itself is read-only. Synthetic tests cover report omission, supported reads, refusal, restart, backup and fresh restore while retaining raw originals. These are data-semantics checks only; native passthrough operations remain unqualified.

## Why guest grow execution remains open

The retained deployed v5.61.58 source (`6db8c7237bd7cf507532391c0dc0c2e36a70f696`) queues only a VM/device pair, repeatedly offers a device path, and clears pending work by VM UUID when a three-field grow report arrives. It does not carry a request/attempt identity or durable guest acknowledgement. Its `/dev/<libvirt target>` mapping does not prove guest device identity. An error-free report cannot establish completion of a particular request.

A synthetic ledger alone would not qualify that legacy protocol. Safe future execution requires a versioned correlated request/outcome protocol, durable guest-side deduplication, verified target identity and supported completion evidence. Original grow reports remain preserved without dispatch or a success claim. This batch therefore takes the authorized passive passthrough fallback. No guest, device or native operation is executed. UI execution, public releases and deployment remain on hold.
