-- The allowlist of internal networks and hosts MCP servers may use
-- (docs/decisions.md, decision 20; docs/registry.md "Security"):
--
--   * network_settings: one row; the platform admins' entries, a JSON array
--     of {kind: cidr|host, value, allowHttp, note?} (canonical, at most 100,
--     validated by internal/netpolicy), with its own revision so editing
--     it never conflicts with the publishing settings. Entries from the
--     deployment (UPSTREAM_ALLOWED_NETWORKS) are not stored.
--
-- A new table only (expand/contract): the previous release keeps working
-- against this schema (it ignores the entries, so internal servers stay
-- refused until this release is deployed).

-- +goose Up
CREATE TABLE network_settings (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    entries    jsonb       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(entries) = 'array'),
    revision   bigint      NOT NULL DEFAULT 1,
    updated_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO network_settings (id) VALUES (1);

-- +goose Down
DROP TABLE network_settings;
