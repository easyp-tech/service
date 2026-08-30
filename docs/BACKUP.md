# Backup and restore

Deliberately tool-agnostic: whatever takes your Postgres backups today takes
these. What follows is what has to be in the set and what the service assumes
about it.

### What has to be backed up

| What | Why it cannot be reconstructed |
|------|-------------------------------|
| **The `plugins` table** | The registry itself: which plugin versions exist, their config, their command line, their recorded checksums. Nothing else holds this. |
| **The `audit_log` partitions** | The audit trail. Enterprise sells it; it exists nowhere else and there is no second copy. |
| **The `goose_db_version` table** | Which migrations have run. Restoring data without it makes the next startup either re-run migrations or refuse to start. |
| **The object storage bucket** | Plugin archives. The local cache is a cache — it is evicted — so the bucket is the only copy of the binaries. |
| **`DB_POSTGRES_DSN`, `AUTH_WRITE_TOKENS`, S3 credentials** | Secrets are not in the database and are usually not in the cluster backup either. |
| **`LICENSE_KEY` and `LICENSE_PUBLIC_KEYS`** | Recoverable from the licence registry, but not by you at 3am. Without them the restored service runs in community mode: no audit, four workers, ten plugins. |

The database and the bucket have to be recoverable to *roughly* the same point.
They are not transactional with each other, and the direction of the skew is what
matters: a `plugins` row without its archive fails generation with
`BINARY_NOT_UPLOADED`, which is at least legible. An archive with no row is
merely orphaned. **So prefer a bucket snapshot slightly newer than the database
one** — never older.

### How much history to keep

`config.audit.retentionMonths` (12 by default) is enforced by dropping whole
partitions on a schedule. That is a real delete: after it, the only copy of that
month is in a backup taken while it still existed.

So the backup retention has to exceed the audit retention, not match it. Matching
them means the month drops out of the database and out of the archive at
approximately the same time, which leaves nothing anywhere. If you are asked to
keep audit history for a compliance window, that window is a constraint on the
*backups*, and `retentionMonths` is only the size of the working set.

An RPO of hours is fine for `plugins` — plugin registration is a deliberate,
infrequent act and is easily repeated. For `audit_log` the RPO *is* the size of
the gap in the audit trail, so it should be measured in minutes if audit is
being relied on.

### Restoring

1. **Restore the database first**, then the bucket. The reverse order leaves the
   service briefly able to serve plugins it has no rows for.
2. **Do not run migrations by hand.** The service applies them itself at startup
   and serialises that across replicas. Start one replica and let it.
3. **Check `goose_db_version` against the binary you are restoring onto.** A
   database restored from a newer release than the binary **starts anyway** —
   this is not caught, and it used to say here that it was. goose objects only
   to migrations missing *below* the database's highest version; one it has
   never heard of, above that mark, leaves it with nothing to apply and no
   complaint. Pinned by `TestRollbackOntoAnOlderBinary`.

   So the limit on running an older binary is not a startup check, it is time.
   The older binary has no partition maintainer for anything migration 00002
   introduced, so once the months that migration pre-created are used up, audit
   rows land in `audit_log_default` — and a non-empty default partition blocks
   creating the month that would overlap it, which is a problem you inherit on
   the way *forward*. **Treat a rollback as bounded by
   `config.audit.preCreateMonths`** (three by default), and prefer restoring
   onto the matching release.
4. **Expect the plugin cache to be empty.** It is a cache, and with
   `persistence.enabled=false` it is empty after every restart anyway. The first
   request for each plugin re-downloads it. Watch
   `easyp_plugin_cache_bytes` climb; nothing needs doing.
5. **Verify audit continuity before declaring done.** Partition maintenance runs
   every `config.audit.partitionCheckInterval`, so a restore landing in a month
   whose partition was never created writes into the default partition —
   `easyp_audit_default_partition_used` goes to 1 and stays there. See
   [RUNBOOKS.md](RUNBOOKS.md).

### Verifying

A backup that has never been restored is a hypothesis. The cheap version of the
test: restore into a scratch namespace, start the service against it, and check
that `easyp_business_plugins_total` matches what production reports and that the
newest `audit_log` row is inside the RPO you think you have.
