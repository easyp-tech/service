# Runbooks

One section per alert in `deploy/charts/easyp-service/templates/prometheusrule.yaml`.
Each alert's `runbook_url` annotation points at the matching anchor here, so the
heading text is load-bearing: renaming one breaks the link from the alert.

Every procedure assumes `kubectl` against the namespace the release is installed
in, and `$REL` as the release name.

---

## EasypLicenceExpiringSoon

The licence stops being valid within `prometheusRule.licenceExpiryWarningDays`.

Nothing breaks at expiry — the token also carries a grace period — but at the end
of that, the tier drops to community: audit stops, workers cap at 4, plugin
registration starts refusing past 10. All of it silent.

1. Confirm what the service actually loaded, rather than what you think it has:
   `kubectl logs deploy/$REL | grep -i licen`
2. Check `easyp_license_expiry_timestamp_seconds` against `time()`.
3. Get a renewed token, put it in the secret as `LICENSE_KEY`, restart the pods.
   The licence is read at startup.

If the token is present but ignored, the usual cause is a missing or mismatched
`LICENSE_PUBLIC_KEY`: a token is only as good as the key it verifies against, and
the key id in the token footer has to be one of the keys configured. See
`config.license.publicKeys`.

## EasypLicenceInGrace

Expiry has passed and the service is running on the grace period the token
granted. This is the last warning before the tier drops.

Same procedure as above, without the slack.

## EasypGenerationsRejected

Generation requests are being refused with `ResourceExhausted`. The service is
doing this deliberately — it is not an error, it is a full queue.

1. `easyp_pool_queue_depth` against `config.workerPool.queueSize`, and
   `easyp_pool_active_workers` against `workers`.
2. Decide which limit is actually binding: workers, `maxConcurrentGenerations`,
   or the per-caller `rateLimit.maxConcurrentPerIP`.
3. Raise the binding one — but check the memory arithmetic first. Peak buffers
   are `maxConcurrentGenerations × maxOutputSize × 2`, and the chart refuses an
   install where that exceeds the memory limit. Raising concurrency without
   raising memory turns rejection into OOMKill, which is strictly worse: an
   OOMKill looks like a crash, and nothing counts it.

If the rejections come from one caller, the per-IP limits are working as
intended. Note that CI runners behind one NAT share an address.

## EasypGenerationQueueSaturated

Work has been queued continuously for ten minutes. Same knobs as above; the
difference is that this is sustained rather than bursty, so it is a capacity
statement, not a spike. Consider more replicas rather than a deeper queue — a
longer queue only moves the latency around.

## EasypPluginCacheAtLimit

Unpacked plugins on disk have reached `config.registry.cacheMaxBytes`.

This is not by itself a fault: the cache is supposed to reach its limit and
evict. Watch `easyp_plugin_cache_evictions_total`. Steady eviction with steady
generation latency means it is working.

It matters when eviction churns — the same plugins evicted and re-downloaded
repeatedly, which shows up as generation latency and S3 egress. In that case the
working set does not fit: raise `cacheMaxBytes`, and the storage behind it. Both
storage paths are checked at install time, so the chart will refuse a
`cacheMaxBytes` that does not fit its volume.

## EasypAuditEventsLost

Audit records are being dropped. Audit is an Enterprise commitment, so any
sustained loss here is a gap in what was promised. The `reason` label says which
failure this is, and they have nothing in common but the counter:

- **`enqueue_timeout`** — the writer is not draining fast enough and callers gave
  up waiting for room. Look at `easyp_audit_queue_depth` and at database write
  latency. Raise `config.audit.bufferSize` to absorb bursts;
  `config.audit.enqueueTimeout` to trade request latency for fewer losses.
- **`save_failed`** — writes are being rejected outright, after
  `maxSaveRetries`. This is a database problem, not a tuning problem. Check
  `easyp_audit_save_failures_total`, then the database itself. A common cause is
  no partition for the current month — see EasypAuditMaintenanceStale.
- **`shutdown_timeout`** — a pod stopped before it finished draining. Occasional
  single-digit losses during a rollout are the queue remainder; anything larger
  means the shutdown budget is too tight for the queue depth.

## EasypAuditMaintenanceStale

Partition maintenance has not succeeded for a day. It runs every
`config.audit.partitionCheckInterval` (6h by default), so this means several
consecutive failures.

Consequence: future partitions stop being created. Once the newest existing
partition is passed, audit rows land in the default partition (see below), and
if there is no default either, writes fail outright.

1. `kubectl logs deploy/$REL | grep -i partition` — the failure is logged with
   the reason.
2. Most often a permissions problem: the role needs to CREATE and DROP tables in
   the schema, not merely write rows.
3. Check `easyp_audit_partition_maintenance_last_success_seconds` recovers after
   the next interval. Do not wait a full 6h to find out — restart the deployment
   to force a run.

## EasypAuditDefaultPartitionUsed

Audit rows have landed in `audit_log_default` — the catch-all partition — because
the partition for their month did not exist when they were written.

**Nothing is lost.** The rows are queryable and complete. Two things are wrong
while they sit there:

- the partition for that month cannot be created at all, because its range
  overlaps rows already in the default;
- Postgres cannot prune partitions for any query filtering on `created_at`, so
  every such query reads the whole audit history. This is what turns a bounded
  scan into a full one.

Fix the cause first — if maintenance is still failing, draining the default only
buys time until the next month. See EasypAuditMaintenanceStale.

Then drain it. Postgres cannot move rows between partitions in place, so the
procedure is detach, create, copy, drop. Run it in a maintenance window: the
detach takes an ACCESS EXCLUSIVE lock on the parent, briefly blocking audit
writes. The audit writer retries, so a short block costs nothing; a long one is
counted as `save_failed`.

```sql
BEGIN;

-- 1. Take the default out of the parent so its rows stop blocking range checks.
ALTER TABLE audit_log DETACH PARTITION audit_log_default;

-- 2. Create the month that was missing. Repeat per month present in the default:
--    SELECT DISTINCT date_trunc('month', created_at) FROM audit_log_default;
CREATE TABLE audit_log_2026_08 PARTITION OF audit_log
  FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

-- 3. Move the rows. They route to the right partition on insert.
INSERT INTO audit_log
SELECT * FROM audit_log_default
WHERE created_at >= '2026-08-01' AND created_at < '2026-09-01';

DELETE FROM audit_log_default
WHERE created_at >= '2026-08-01' AND created_at < '2026-09-01';

-- 4. Reattach only once it is empty. A non-empty default reattaches fine but
--    puts you back where you started.
ALTER TABLE audit_log ATTACH PARTITION audit_log_default DEFAULT;

COMMIT;
```

Confirm `easyp_audit_default_partition_used` returns to 0 within one scrape.

The metric is `SELECT EXISTS(...)`, not a count, so it flips the moment the last
row leaves — it will not tell you how far through you are. Use
`SELECT count(*) FROM audit_log_default` for that.

If the default holds enough rows that the transaction above is impractical, do
it a month at a time with the detach and reattach as separate transactions.

## EasypGenerationErrorRate

More than `prometheusRule.generationErrorRatio` of generations are failing.

1. `easyp_generation_errors_total` by `plugin` and `error_type` — this is almost
   always one plugin, not a general fault.
2. If `error_type` names a checksum mismatch, the binary on disk does not match
   what was recorded for that plugin version. Do not "fix" it by re-registering:
   find out why it changed.
3. Otherwise, run the plugin by hand with the same request. Plugin failures are
   reported faithfully, so the plugin's own stderr is in the logs.

## EasypPanics

The service recovered from a panic. It kept running — that is what the barrier is
for — but a recovered panic is still a bug, and the counter exists so that it is
not silent.

`kubectl logs deploy/$REL | grep -A30 panic` for the stack. The barrier's `name`
label says which unit of work it was: `worker.process_job` for generation,
`audit.flush` for the audit writer.

## EasypAuthFailures

Write credentials are being rejected faster than
`prometheusRule.authFailureRate` per second.

Reads are anonymous by design, so everything here is a mutating call:
CreatePlugin, UpdatePlugin, DeletePlugin.

1. A rollout of CI credentials that did not land is the usual cause — check
   whether the rate started at a deploy.
2. `AUTH_WRITE_TOKENS` maps `name=sha256`; the logs name the token that failed,
   not the secret.
3. If it is not a known caller, it is someone trying tokens against the
   registry. The tokens are hashed and the rate limiter applies, but this is
   worth knowing about.
