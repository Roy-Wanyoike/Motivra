-- Authored inspection templates (issue #184): the per-tenant authoring
-- lifecycle on top of the code-owned system registry (templates.go). A
-- template moves draft → published → retired:
--
--   draft     content editable; invisible to non-author roles;
--   published hash-pinned: content_hash holds the SHA-256 of the canonical
--             content JSON at publish time; content, name and hash become
--             immutable (guard trigger below); one published version per
--             (tenant_id, name);
--   retired   readable for historical references but unusable for new
--             sessions; no resurrection (retired is terminal).
--
-- Sessions and reports that consume an authored template reference the
-- exact (id, content_hash) pair so later registry changes never reinterpret
-- a historical inspection.
CREATE TABLE inspection_authored_templates (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    name         text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    status       text NOT NULL CHECK (status IN ('draft','published','retired')),
    content      jsonb NOT NULL,
    content_hash text,
    created_by   uuid NOT NULL,
    published_at timestamptz,
    retired_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'draft' AND content_hash IS NULL AND published_at IS NULL AND retired_at IS NULL)
        OR (status = 'published' AND content_hash IS NOT NULL AND published_at IS NOT NULL AND retired_at IS NULL)
        OR (status = 'retired' AND content_hash IS NOT NULL AND published_at IS NOT NULL AND retired_at IS NOT NULL))
);

-- One published version per (tenant_id, name): publishing a new version of
-- the same checklist retires nothing automatically — it conflicts until the
-- old version is retired explicitly (explicit lifecycle, no silent swaps).
CREATE UNIQUE INDEX inspection_authored_templates_published_idx
    ON inspection_authored_templates (tenant_id, name)
    WHERE status = 'published';

CREATE INDEX inspection_authored_templates_tenant_idx
    ON inspection_authored_templates (tenant_id, status, created_at DESC);

-- Immutability + terminal-state guard: after publish only the status may
-- move (published → retired); retired rows never change again. Content,
-- name and hash are frozen the moment the row leaves draft.
CREATE OR REPLACE FUNCTION inspection_authored_templates_guard() RETURNS trigger AS $$
BEGIN
    IF OLD.status <> 'draft' THEN
        IF NEW.content IS DISTINCT FROM OLD.content
           OR NEW.name IS DISTINCT FROM OLD.name
           OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
           OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id THEN
            RAISE EXCEPTION 'inspection_authored_templates: published templates are immutable (hash-pinned)';
        END IF;
        IF NEW.status NOT IN ('published','retired')
           OR (OLD.status = 'retired' AND NEW.status <> 'retired') THEN
            RAISE EXCEPTION 'inspection_authored_templates: invalid status transition % -> %', OLD.status, NEW.status;
        END IF;
    END IF;
    IF OLD.status = 'draft' AND NEW.status NOT IN ('draft','published') THEN
        RAISE EXCEPTION 'inspection_authored_templates: invalid status transition % -> %', OLD.status, NEW.status;
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_authored_templates_guard_trg
    BEFORE UPDATE ON inspection_authored_templates
    FOR EACH ROW EXECUTE FUNCTION inspection_authored_templates_guard();
