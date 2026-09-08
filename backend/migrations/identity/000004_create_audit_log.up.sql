-- Identity domain: append-only audit trail (ADR-0003 append-only rules,
-- ADR-0004 privileged-action auditing).
CREATE TABLE audit_log (
    id          bigserial   PRIMARY KEY,
    actor_id    uuid,
    action      text        NOT NULL,
    object_type text        NOT NULL,
    object_id   text        NOT NULL,
    tenant_id   uuid,
    metadata    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_actor_created_idx ON audit_log (actor_id, created_at);
