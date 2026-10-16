-- The MCP server registry (SPEC.md §5, §7 phase 2; docs/registry.md):
--
--   * mcp_servers: remote MCP servers a team registered (Streamable HTTP),
--     with an optional static header whose value is sealed with
--     ENCRYPTION_KEY (internal/secrets, bound to the row's ID), and their
--     publish state for the company catalog.
--   * mcp_capabilities: the tools, resources, resource templates and
--     prompts each server listed at its last refresh. A capability the
--     server no longer lists is marked gone, not deleted. approved means
--     approved_fingerprint = fingerprint (and not gone), so a changed
--     definition is unapproved without anyone touching the approval.
--   * mcp_health_checks: manual (test, refresh) and scheduled checks, the
--     latest 100 per server. status_since is copied forward while the
--     result stays the same, so "failing since" survives pruning.
--   * platform_settings: one row; the default publishing policy.
--   * teams.publish_policy_override: a team's policy when it differs from
--     the default (NULL: follow the default).
--
-- New tables and a nullable column only (expand/contract): the previous
-- release keeps working against this schema.

-- +goose Up
ALTER TABLE teams ADD COLUMN publish_policy_override text
    CHECK (publish_policy_override IN ('approval_required', 'self_publish'));

CREATE TABLE platform_settings (
    id                     smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    publish_default_policy text        NOT NULL DEFAULT 'approval_required'
        CHECK (publish_default_policy IN ('approval_required', 'self_publish')),
    revision               bigint      NOT NULL DEFAULT 1,
    updated_by             uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at             timestamptz NOT NULL DEFAULT now()
);
INSERT INTO platform_settings (id) VALUES (1);

CREATE TABLE mcp_servers (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id              uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    -- Globally unique and immutable: it prefixes the server's tool names in
    -- gateways (slug__tool).
    slug                 text        NOT NULL CONSTRAINT mcp_servers_slug_key UNIQUE
        CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$'),
    name                 text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description          text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    url                  text        NOT NULL CHECK (url ~ '^https?://' AND char_length(url) <= 500),
    transport            text        NOT NULL DEFAULT 'streamable_http' CHECK (transport IN ('streamable_http')),
    -- A static header sent with every request; its value sealed. Both NULL
    -- when the server needs no credentials. The hint is the value's last
    -- four characters (empty for short values).
    auth_header_name     text        CHECK (auth_header_name ~ '^[A-Za-z0-9!#$%&''*+.^_`|~-]{1,100}$'),
    auth_value_cipher    bytea,
    auth_value_hint      text        NOT NULL DEFAULT '' CHECK (char_length(auth_value_hint) <= 4),
    timeout_seconds      integer     NOT NULL DEFAULT 30 CHECK (timeout_seconds BETWEEN 1 AND 120),
    enabled              boolean     NOT NULL DEFAULT true,
    publish_state        text        NOT NULL DEFAULT 'private'
        CHECK (publish_state IN ('private', 'pending', 'published', 'rejected')),
    publish_requested_at timestamptz,
    publish_requested_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    publish_request_note text        CHECK (char_length(publish_request_note) <= 2000),
    publish_reviewed_at  timestamptz,
    publish_reviewed_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    publish_review_note  text        CHECK (char_length(publish_review_note) <= 2000),
    published_at         timestamptz,
    -- When capabilities were last read (NULL: never), and why the latest
    -- attempt failed (NULL after a successful one).
    refreshed_at         timestamptz,
    last_refresh_error   text        CHECK (char_length(last_refresh_error) <= 500),
    revision             bigint      NOT NULL DEFAULT 1,
    created_by           uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mcp_servers_auth_pair CHECK ((auth_header_name IS NULL) = (auth_value_cipher IS NULL)),
    CONSTRAINT mcp_servers_published_at CHECK (publish_state <> 'published' OR published_at IS NOT NULL)
);
CREATE INDEX mcp_servers_team_idx ON mcp_servers (team_id, name, id);
CREATE INDEX mcp_servers_name_idx ON mcp_servers (name, id);
CREATE INDEX mcp_servers_catalog_idx ON mcp_servers (name, id) WHERE publish_state = 'published' AND enabled;

CREATE TABLE mcp_capabilities (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id            uuid        NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    kind                 text        NOT NULL CHECK (kind IN ('tool', 'resource', 'resource_template', 'prompt')),
    -- Tool or prompt name, resource URI or URI template.
    name                 text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 1000),
    title                text        NOT NULL DEFAULT '' CHECK (char_length(title) <= 200),
    description          text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 10000),
    -- The listed object, canonical JSON without _meta.
    definition           jsonb       NOT NULL,
    -- SHA-256 hex of the canonical definition.
    fingerprint          text        NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    -- The fingerprint someone approved (NULL: never approved, or cleared
    -- when the capability went away).
    approved_fingerprint text        CHECK (approved_fingerprint ~ '^[0-9a-f]{64}$'),
    approved_by          uuid        REFERENCES users (id) ON DELETE SET NULL,
    approved_at          timestamptz,
    first_seen_at        timestamptz NOT NULL DEFAULT now(),
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    gone_at              timestamptz,
    CONSTRAINT mcp_capabilities_server_kind_name_key UNIQUE (server_id, kind, name),
    CONSTRAINT mcp_capabilities_gone_unapproved CHECK (gone_at IS NULL OR approved_fingerprint IS NULL)
);

CREATE TABLE mcp_health_checks (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id    uuid        NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    checked_at   timestamptz NOT NULL DEFAULT now(),
    ok           boolean     NOT NULL,
    latency_ms   integer     NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    trigger      text        NOT NULL CHECK (trigger IN ('manual', 'scheduled')),
    -- Who pressed Test or Refresh (manual checks only).
    triggered_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    error_class  text        CHECK (error_class IN ('unavailable', 'auth', 'not_found', 'rate_limited', 'bad_request',
                                                    'bad_response', 'config', 'timeout', 'too_large', 'not_allowed')),
    http_status  integer     CHECK (http_status BETWEEN 100 AND 599),
    -- A short message safe to show the team: never a header value or a raw body.
    message      text        NOT NULL DEFAULT '' CHECK (char_length(message) <= 500),
    -- When the current status (ok or not) began.
    status_since timestamptz NOT NULL,
    CONSTRAINT mcp_health_checks_error_class CHECK (ok = (error_class IS NULL)),
    CONSTRAINT mcp_health_checks_scheduled_by CHECK (trigger = 'manual' OR triggered_by IS NULL)
);
CREATE INDEX mcp_health_checks_server_idx ON mcp_health_checks (server_id, checked_at DESC, id DESC);

-- +goose Down
DROP TABLE mcp_health_checks;
DROP TABLE mcp_capabilities;
DROP TABLE mcp_servers;
DROP TABLE platform_settings;
ALTER TABLE teams DROP COLUMN publish_policy_override;
