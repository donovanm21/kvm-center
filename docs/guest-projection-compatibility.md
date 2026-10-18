# Guest report expiry and VM projections

The retained v5.61.58 source contract (`6db8c7237bd7cf507532391c0dc0c2e36a70f696`) defines a five-minute guest-report freshness window using server receipt time. Its `server/store.go` SHA-256 is `e8cd19d25247231dbc59686f127269437cc4b8dd8703d6ec5011e12bbf05cad8`, matching the deployed-source manifest. This is source provenance, not native execution qualification.

VM list and single reads now project onto copies under the store read lock. A report is current only when its receipt time is nonzero, not in the future, and no more than five minutes old. A previously cached `kvm-tools` projection is cleared before applying a current report, so expired data and fields omitted by a later report cannot remain visible as current. Guest slices, including nested interface addresses, are copied. Reads do not refresh timestamps, delete reports, alter raw BSON, save state, or consume queued instructions. The update-queued indicator reflects the existing queue independently of report freshness.

When no current tools report exists, an existing `qemu-ga` projection is preserved. Older qemu-ga values overwritten by a stored kvm-tools projection cannot be reconstructed; the read returns no current tools data until new evidence arrives. Unknown source labels are retained without inventing their semantics.

Synthetic acceptance retains an expired nested-opaque BSON report and a cached kvm-tools VM projection, then checks list/single reads through application restart, backup, fresh-database restore and a second restart. TTL remains inactive in the dedicated preservation service. Original report bytes and timestamps are retained. Unit tests exercise receipt-time boundaries, missing/future/zero timestamps, omitted fields, queue isolation and returned-copy isolation.

This closes only the bounded expiry/read-projection gap. Update execution, grow execution/results semantics, full guest operational compatibility, full-schema upgrade activation and post-upgrade old-writer rollback remain unproven. Native/UI execution, release and deployment holds remain unchanged.
