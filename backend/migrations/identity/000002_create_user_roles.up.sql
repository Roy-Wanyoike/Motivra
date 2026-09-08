-- Identity domain: role grants (ADR-0004 fixed role set).
-- A role grant is either personal (tenant_id NULL) or organization-scoped
-- (tenant_id set). Surrogate bigserial key because a PostgreSQL primary key
-- cannot contain NULL tenant_id values.
CREATE TABLE user_roles (
    id         bigserial   PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text        NOT NULL
               CHECK (role IN ('CUSTOMER', 'TECHNICIAN', 'DISPATCHER', 'GARAGE',
                               'FLEET_ADMIN', 'SUPPORT', 'FINANCE', 'ADMIN',
                               'SUPER_ADMIN')),
    tenant_id  uuid,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT user_roles_user_role_tenant_key UNIQUE (user_id, role, tenant_id)
);

-- UNIQUE treats NULLs as distinct, so personal grants (tenant_id NULL) get
-- their own partial unique index to keep (user_id, role) truly unique.
CREATE UNIQUE INDEX user_roles_personal_key
    ON user_roles (user_id, role)
    WHERE tenant_id IS NULL;

CREATE INDEX user_roles_user_id_idx ON user_roles (user_id);
