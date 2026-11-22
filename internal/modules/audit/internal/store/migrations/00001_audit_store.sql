-- Audit store (ADR-0008): hash-chained, append-only audit events per chain
-- (one chain per tenant; the nil UUID is the platform chain), signed
-- checkpoints, retention marks and the published checkpoint signing keys.
--
-- Append-only: no role has UPDATE or DELETE on the history tables, and a
-- trigger rejects updates and deletes as a second line of defence. The only
-- removal path is audit.apply_retention, a SECURITY DEFINER function owned by
-- the owner role, which records a retention mark in the same transaction.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE audit.chain_heads (
    chain_id   uuid        PRIMARY KEY, -- the tenant ID; the nil UUID is the platform chain
    sequence   bigint      NOT NULL CHECK (sequence >= 0),
    hash       text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit.events (
    tenant_id   uuid        NOT NULL,
    recorded_at timestamptz NOT NULL,
    sequence    bigint      NOT NULL CHECK (sequence > 0),
    id          uuid        NOT NULL,
    occurred_at timestamptz NOT NULL,
    category    text        NOT NULL,
    event_type  text        NOT NULL,
    outcome     text        NOT NULL,
    actor_type  text        NOT NULL DEFAULT '',
    actor_id    text        NOT NULL DEFAULT '',
    target_type text        NOT NULL DEFAULT '',
    target_id   text        NOT NULL DEFAULT '',
    channel     text        NOT NULL DEFAULT '',
    request_id  text        NOT NULL DEFAULT '',
    trace_id    text        NOT NULL DEFAULT '',
    event       jsonb       NOT NULL, -- the record with sequence and integrity
    hash        text        NOT NULL,
    prev_hash   text        NOT NULL,
    PRIMARY KEY (tenant_id, recorded_at, sequence)
) PARTITION BY RANGE (recorded_at);
CREATE INDEX events_sequence ON audit.events (tenant_id, sequence);
CREATE INDEX events_occurred ON audit.events (tenant_id, occurred_at);
CREATE INDEX events_type ON audit.events (tenant_id, event_type, sequence);
CREATE INDEX events_actor ON audit.events (tenant_id, actor_id, sequence);
CREATE INDEX events_target ON audit.events (tenant_id, target_id, sequence);
CREATE INDEX events_request ON audit.events (tenant_id, request_id) WHERE request_id <> '';
CREATE INDEX events_trace ON audit.events (tenant_id, trace_id) WHERE trace_id <> '';

-- Deduplication by event ID, and a global uniqueness guarantee for
-- (chain, sequence), which the partitioned table cannot enforce itself.
CREATE TABLE audit.event_ids (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL,
    sequence    bigint      NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, sequence)
);

CREATE TABLE audit.checkpoints (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL,
    from_seq    bigint      NOT NULL CHECK (from_seq > 0),
    to_seq      bigint      NOT NULL,
    root        text        NOT NULL,
    prev_id     uuid,
    created_at  timestamptz NOT NULL,
    tree_head   bytea       NOT NULL, -- the signed JCS bytes
    key_ref     text        NOT NULL,
    key_version integer     NOT NULL CHECK (key_version > 0),
    signature   bytea       NOT NULL, -- ASN.1 DER ECDSA P-256
    tsa_token   bytea,                -- RFC 3161 TimeStampToken over the signature
    tsa_time    timestamptz,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, from_seq),
    UNIQUE (tenant_id, to_seq),
    CHECK (to_seq >= from_seq)
);

CREATE TABLE audit.retention_marks (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL,
    from_seq    bigint      NOT NULL CHECK (from_seq > 0),
    through_seq bigint      NOT NULL,
    last_hash   text        NOT NULL,
    removed_at  timestamptz NOT NULL,
    payload     bytea       NOT NULL, -- the signed JCS bytes
    key_ref     text        NOT NULL,
    key_version integer     NOT NULL CHECK (key_version > 0),
    signature   bytea       NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, through_seq),
    CHECK (through_seq >= from_seq)
);

-- Public halves of the checkpoint signing keys, by version (public data).
CREATE TABLE audit.signing_keys (
    key_ref    text        NOT NULL,
    version    integer     NOT NULL CHECK (version > 0),
    public_key bytea       NOT NULL, -- PKIX DER
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key_ref, version)
);

CREATE TABLE audit.retention_policies (
    chain_id       uuid        PRIMARY KEY,
    retention_days integer     CHECK (retention_days IS NULL OR retention_days > 0),
    legal_hold     boolean     NOT NULL DEFAULT false,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Messages the writer could not chain (malformed, wrong subject): kept, never dropped.
CREATE TABLE audit.rejected_messages (
    message_id  text        NOT NULL,
    subject     text        NOT NULL,
    stream_seq  bigint      NOT NULL,
    payload     bytea       NOT NULL,
    reason      text        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subject, stream_seq)
);

-- Row-level security: tenants see only their own chain. The platform chain
-- (nil UUID) is never a tenant scope, so only portun_platform (BYPASSRLS)
-- reads it.
ALTER TABLE audit.chain_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.chain_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit.chain_heads
    USING (chain_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (chain_id = current_setting('app.tenant_id')::uuid);
ALTER TABLE audit.events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit.events
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
ALTER TABLE audit.event_ids ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.event_ids FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit.event_ids
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
ALTER TABLE audit.checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.checkpoints FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit.checkpoints
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
ALTER TABLE audit.retention_marks ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.retention_marks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit.retention_marks
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- Append-only enforcement. Deletes pass only on the retention path: inside
-- audit.apply_retention (current_user is then the owner) with the
-- transaction-local marker it sets.
CREATE FUNCTION audit.reject_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
    IF TG_OP = 'DELETE' AND TG_NARGS > 0 AND TG_ARGV[0] = 'retention'
       AND current_setting('portun.audit_retention', true) = 'on'
       AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c
                            WHERE c.oid = 'audit.events'::regclass) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit records are append-only: % on %.% rejected', TG_OP, TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = 'insufficient_privilege';
END
$fn$;

CREATE TRIGGER append_only BEFORE UPDATE OR DELETE ON audit.events
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change('retention');
CREATE TRIGGER append_only BEFORE UPDATE OR DELETE ON audit.event_ids
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change('retention');
CREATE TRIGGER append_only BEFORE UPDATE OR DELETE ON audit.checkpoints
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change();
CREATE TRIGGER append_only BEFORE UPDATE OR DELETE ON audit.retention_marks
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change();
CREATE TRIGGER append_only BEFORE UPDATE OR DELETE ON audit.signing_keys
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change();
CREATE TRIGGER append_only BEFORE UPDATE OR DELETE ON audit.rejected_messages
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change();

CREATE FUNCTION audit.reject_truncate() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
    RAISE EXCEPTION 'audit records are append-only: TRUNCATE on %.% rejected', TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = 'insufficient_privilege';
END
$fn$;
CREATE TRIGGER no_truncate BEFORE TRUNCATE ON audit.events FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_truncate();
CREATE TRIGGER no_truncate BEFORE TRUNCATE ON audit.event_ids FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_truncate();
CREATE TRIGGER no_truncate BEFORE TRUNCATE ON audit.checkpoints FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_truncate();
CREATE TRIGGER no_truncate BEFORE TRUNCATE ON audit.retention_marks FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_truncate();
CREATE TRIGGER no_truncate BEFORE TRUNCATE ON audit.signing_keys FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_truncate();

-- Monthly partitions, created ahead by the writer: the month of p_at and the
-- next one. Each partition gets the tenant policy too.
CREATE FUNCTION audit.ensure_partitions(p_at timestamptz) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
    m     timestamptz;
    pname text;
BEGIN
    PERFORM pg_advisory_xact_lock(7381904471230001);
    FOR i IN 0..1 LOOP
        m := date_trunc('month', p_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + make_interval(months => i);
        pname := 'events_' || to_char(m AT TIME ZONE 'UTC', 'YYYY_MM');
        IF to_regclass('audit.' || quote_ident(pname)) IS NULL THEN
            EXECUTE format('CREATE TABLE audit.%I PARTITION OF audit.events FOR VALUES FROM (%L) TO (%L)',
                           pname, m, m + interval '1 month');
            EXECUTE format('ALTER TABLE audit.%I ENABLE ROW LEVEL SECURITY', pname);
            EXECUTE format('ALTER TABLE audit.%I FORCE ROW LEVEL SECURITY', pname);
            EXECUTE format('CREATE POLICY tenant_isolation ON audit.%I '
                           'USING (tenant_id = current_setting(''app.tenant_id'')::uuid) '
                           'WITH CHECK (tenant_id = current_setting(''app.tenant_id'')::uuid)', pname);
        END IF;
    END LOOP;
END
$fn$;

-- The only removal path. It checks the plan against the stored chain,
-- records the signed retention mark and removes exactly the range, all in
-- the caller's transaction.
CREATE FUNCTION audit.apply_retention(
    p_chain uuid, p_id uuid, p_from bigint, p_through bigint, p_last_hash text,
    p_removed_at timestamptz, p_payload bytea, p_key_ref text, p_key_version integer, p_signature bytea
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
    v_hold      boolean;
    v_expected  bigint;
    v_hash      text;
    v_removed   bigint;
    v_ids       bigint;
    v_scope     text := current_setting('app.tenant_id', true);
BEGIN
    -- The owner is subject to the forced row-level security policies: scope
    -- this transaction to the chain for the duration of the call.
    PERFORM set_config('app.tenant_id', p_chain::text, true);
    SELECT legal_hold INTO v_hold FROM audit.retention_policies WHERE chain_id = p_chain FOR SHARE;
    IF coalesce(v_hold, false) THEN
        RAISE EXCEPTION 'audit chain % is under legal hold', p_chain USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    SELECT coalesce(max(through_seq), 0) + 1 INTO v_expected FROM audit.retention_marks WHERE tenant_id = p_chain;
    IF p_from <> v_expected OR p_through < p_from THEN
        RAISE EXCEPTION 'retention range %..% does not continue at %', p_from, p_through, v_expected
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM audit.checkpoints WHERE tenant_id = p_chain AND to_seq = p_through) THEN
        RAISE EXCEPTION 'retention must end at a checkpoint boundary' USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    SELECT hash INTO v_hash FROM audit.events WHERE tenant_id = p_chain AND sequence = p_through;
    IF v_hash IS NULL OR v_hash <> p_last_hash THEN
        RAISE EXCEPTION 'retention mark hash does not match the stored chain' USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    INSERT INTO audit.retention_marks (tenant_id, id, from_seq, through_seq, last_hash, removed_at, payload, key_ref, key_version, signature)
    VALUES (p_chain, p_id, p_from, p_through, p_last_hash, p_removed_at, p_payload, p_key_ref, p_key_version, p_signature);
    PERFORM set_config('portun.audit_retention', 'on', true);
    DELETE FROM audit.events WHERE tenant_id = p_chain AND sequence BETWEEN p_from AND p_through;
    GET DIAGNOSTICS v_removed = ROW_COUNT;
    DELETE FROM audit.event_ids WHERE tenant_id = p_chain AND sequence BETWEEN p_from AND p_through;
    GET DIAGNOSTICS v_ids = ROW_COUNT;
    PERFORM set_config('portun.audit_retention', 'off', true);
    IF v_removed <> p_through - p_from + 1 OR v_ids <> v_removed THEN
        RAISE EXCEPTION 'retention range %..% is not complete in the store (% events)', p_from, p_through, v_removed
            USING ERRCODE = 'data_corrupted';
    END IF;
    PERFORM set_config('app.tenant_id', coalesce(v_scope, ''), true);
    RETURN v_removed;
END
$fn$;

REVOKE ALL ON FUNCTION audit.ensure_partitions(timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION audit.apply_retention(uuid, uuid, bigint, bigint, text, timestamptz, bytea, text, integer, bytea) FROM PUBLIC;

GRANT USAGE ON SCHEMA audit TO portun_app, portun_platform;
GRANT SELECT ON audit.chain_heads, audit.events, audit.checkpoints, audit.retention_marks, audit.signing_keys TO portun_app;
GRANT SELECT, INSERT ON audit.events, audit.event_ids, audit.checkpoints, audit.signing_keys, audit.rejected_messages TO portun_platform;
GRANT SELECT ON audit.retention_marks TO portun_platform;
GRANT SELECT, INSERT, UPDATE ON audit.chain_heads, audit.retention_policies TO portun_platform;
GRANT EXECUTE ON FUNCTION audit.ensure_partitions(timestamptz) TO portun_platform;
GRANT EXECUTE ON FUNCTION audit.apply_retention(uuid, uuid, bigint, bigint, text, timestamptz, bytea, text, integer, bytea) TO portun_platform;

SELECT audit.ensure_partitions(now());
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION audit.apply_retention(uuid, uuid, bigint, bigint, text, timestamptz, bytea, text, integer, bytea);
DROP FUNCTION audit.ensure_partitions(timestamptz);
DROP TABLE audit.rejected_messages, audit.retention_policies, audit.signing_keys, audit.retention_marks,
    audit.checkpoints, audit.event_ids, audit.events, audit.chain_heads;
DROP FUNCTION audit.reject_truncate();
DROP FUNCTION audit.reject_change();
-- +goose StatementEnd
