-- +goose Up
-- Convert audit_log into a monthly RANGE-partitioned table on created_at so
-- retention can drop whole months instead of running long DELETEs.
--
-- PostgreSQL cannot partition an existing table in place, so the table is
-- rebuilt and swapped. This runs inside goose's per-migration transaction on
-- purpose: a failure between DROP and RENAME would otherwise leave the service
-- with no audit_log at all.
SET LOCAL TimeZone = 'UTC';

CREATE TABLE audit_log_new
(
    id             UUID        NOT NULL DEFAULT gen_random_uuid(),
    operation_type TEXT        NOT NULL,
    plugin_name    TEXT,
    caller_address TEXT        NOT NULL,
    status         TEXT        NOT NULL,
    error_code     TEXT,
    error_message  TEXT,
    duration_ms    BIGINT      NOT NULL,
    metadata       JSONB       NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Every UNIQUE/PK constraint on a partitioned table must contain the
    -- partition key, so PRIMARY KEY (id) becomes (id, created_at). Named
    -- audit_log_pk rather than audit_log_pkey because the legacy table still
    -- owns that index name at this point.
    CONSTRAINT audit_log_pk PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- One partition per month that already holds rows, plus the current month and
-- the next three. Bounds are absolute instants anchored to UTC so they match
-- the ones the Go maintainer generates.
-- +goose StatementBegin
DO $$
DECLARE
    month_start DATE;
    part_name   TEXT;
    lo          TIMESTAMPTZ;
    hi          TIMESTAMPTZ;
BEGIN
    FOR month_start IN
        SELECT date_trunc('month', created_at AT TIME ZONE 'UTC')::date
        FROM audit_log
        UNION
        SELECT (date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => g))::date
        FROM generate_series(0, 3) AS g
        ORDER BY 1
    LOOP
        part_name := format('audit_log_y%sm%s',
                            to_char(month_start, 'YYYY'),
                            to_char(month_start, 'MM'));
        lo := month_start::timestamp AT TIME ZONE 'UTC';
        hi := (month_start + INTERVAL '1 month')::timestamp AT TIME ZONE 'UTC';

        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF audit_log_new FOR VALUES FROM (%L) TO (%L)',
            part_name, lo, hi
        );
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Tripwire, not a landing zone: a row whose created_at falls outside every
-- declared range lands here instead of failing the INSERT and silently losing
-- an audit record. It must stay empty — a non-empty default partition blocks
-- creating the month that would overlap it. Its name deliberately does not
-- match audit_log_yYYYYmMM, so retention can never select it for dropping.
CREATE TABLE audit_log_default PARTITION OF audit_log_new DEFAULT;

INSERT INTO audit_log_new (id, operation_type, plugin_name, caller_address, status,
                           error_code, error_message, duration_ms, metadata, created_at)
SELECT id, operation_type, plugin_name, caller_address, status,
       error_code, error_message, duration_ms, metadata, created_at
FROM audit_log;

-- Frees audit_log, audit_log_pkey and both idx_audit_log_* names.
DROP TABLE audit_log;

ALTER TABLE audit_log_new RENAME TO audit_log;

-- Partitioned indexes: Postgres clones these onto every existing partition and
-- onto every partition created later, so the maintainer never creates indexes.
-- Note CREATE INDEX CONCURRENTLY is unsupported on partitioned tables, so any
-- future index change here will lock every partition.
CREATE INDEX idx_audit_log_created_at ON audit_log (created_at);
CREATE INDEX idx_audit_log_operation_type ON audit_log (operation_type);

-- +goose Down
-- Lossy by nature: months already reaped by retention cannot come back, and the
-- restored PRIMARY KEY (id) will reject duplicate ids should any exist across
-- partitions (which rolls the whole Down back — the safe outcome).
SET LOCAL TimeZone = 'UTC';

CREATE TABLE audit_log_plain
(
    id             UUID        NOT NULL DEFAULT gen_random_uuid(),
    operation_type TEXT        NOT NULL,
    plugin_name    TEXT,
    caller_address TEXT        NOT NULL,
    status         TEXT        NOT NULL,
    error_code     TEXT,
    error_message  TEXT,
    duration_ms    BIGINT      NOT NULL,
    metadata       JSONB       NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT audit_log_plain_pk PRIMARY KEY (id)
);

INSERT INTO audit_log_plain (id, operation_type, plugin_name, caller_address, status,
                             error_code, error_message, duration_ms, metadata, created_at)
SELECT id, operation_type, plugin_name, caller_address, status,
       error_code, error_message, duration_ms, metadata, created_at
FROM audit_log;

-- Dropping a partitioned parent drops all of its partitions.
DROP TABLE audit_log;

ALTER TABLE audit_log_plain RENAME TO audit_log;

CREATE INDEX idx_audit_log_created_at ON audit_log (created_at);
CREATE INDEX idx_audit_log_operation_type ON audit_log (operation_type);
