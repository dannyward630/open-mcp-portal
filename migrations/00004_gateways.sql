-- Gateways (SPEC.md §5-6, §7 phase 3; docs/phase3-gateways.md):
--
--   * users.groups: the IdP groups of the person's latest sign-in (the
--     OIDC_GROUPS_CLAIM claim, bounded), for group grants.
--   * gateways: curated collections served at /mcp/{slug}. A team gateway
--     belongs to a team (team_id); a platform gateway to the platform.
--     capture_payloads is for Phase 6 and is never true yet.
--   * gateway_capabilities: the tools and prompts a gateway exposes.
--     min_team_role is only set on team gateways (enforced by the service:
--     a CHECK can't see the gateway's owner kind).
--   * gateway_grants: who may use a platform gateway: a team (its members),
--     a user, or an SSO group (exactly one).
--   * api_keys: personal keys for /mcp/{slug}. Only an HMAC-SHA256 digest
--     (keyed with API_KEY_PEPPER) is stored, never the key.
--   * signing_keys: the ES256 keys that sign identity JWTs for upstream
--     servers; private keys sealed with ENCRYPTION_KEY, public keys as JWKs.
--   * mcp_calls: append-only metadata of every list and call through a
--     gateway. Never arguments or results.
--
-- New tables and a defaulted column only (expand/contract): the previous
-- release keeps working against this schema.

-- +goose Up
ALTER TABLE users ADD COLUMN groups text[] NOT NULL DEFAULT '{}'
    CONSTRAINT users_groups_bounded CHECK (cardinality(groups) <= 200);

CREATE TABLE gateways (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Unique across the platform and immutable: the endpoint is /mcp/{slug}.
    slug             text        NOT NULL CONSTRAINT gateways_slug_key UNIQUE
        CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$'),
    name             text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description      text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    owner_kind       text        NOT NULL CHECK (owner_kind IN ('team', 'platform')),
    team_id          uuid        REFERENCES teams (id) ON DELETE CASCADE,
    enabled          boolean     NOT NULL DEFAULT true,
    -- Phase 6 (payload capture); stored now so the column exists.
    capture_payloads boolean     NOT NULL DEFAULT false,
    revision         bigint      NOT NULL DEFAULT 1,
    created_by       uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_by       uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT gateways_team_owner CHECK ((owner_kind = 'team') = (team_id IS NOT NULL))
);
CREATE INDEX gateways_team_idx ON gateways (team_id, name, id) WHERE team_id IS NOT NULL;
CREATE INDEX gateways_name_idx ON gateways (name, id);

CREATE TABLE gateway_capabilities (
    gateway_id    uuid        NOT NULL REFERENCES gateways (id) ON DELETE CASCADE,
    capability_id uuid        NOT NULL REFERENCES mcp_capabilities (id) ON DELETE CASCADE,
    -- Team gateways only: the lowest team role that may use it (NULL: every member).
    min_team_role text        CHECK (min_team_role IN ('owner', 'admin', 'editor', 'member')),
    added_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    added_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (gateway_id, capability_id)
);
CREATE INDEX gateway_capabilities_capability_idx ON gateway_capabilities (capability_id);

CREATE TABLE gateway_grants (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    gateway_id   uuid        NOT NULL REFERENCES gateways (id) ON DELETE CASCADE,
    subject_kind text        NOT NULL CHECK (subject_kind IN ('team', 'user', 'group')),
    team_id      uuid        REFERENCES teams (id) ON DELETE CASCADE,
    user_id      uuid        REFERENCES users (id) ON DELETE CASCADE,
    -- An IdP group name as it appears in the groups claim (case-sensitive).
    group_name   text        CHECK (char_length(group_name) BETWEEN 1 AND 256),
    added_by     uuid        REFERENCES users (id) ON DELETE SET NULL,
    added_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT gateway_grants_subject CHECK (
        (subject_kind = 'team') = (team_id IS NOT NULL)
        AND (subject_kind = 'user') = (user_id IS NOT NULL)
        AND (subject_kind = 'group') = (group_name IS NOT NULL))
);
CREATE UNIQUE INDEX gateway_grants_team_key ON gateway_grants (gateway_id, team_id) WHERE team_id IS NOT NULL;
CREATE UNIQUE INDEX gateway_grants_user_key ON gateway_grants (gateway_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX gateway_grants_group_key ON gateway_grants (gateway_id, group_name) WHERE group_name IS NOT NULL;
CREATE INDEX gateway_grants_team_idx ON gateway_grants (team_id) WHERE team_id IS NOT NULL;
CREATE INDEX gateway_grants_user_idx ON gateway_grants (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX gateway_grants_group_idx ON gateway_grants (group_name) WHERE group_name IS NOT NULL;

CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- The key's first characters, to recognise it: omcp_ and 8 more.
    prefix       text        NOT NULL CHECK (prefix ~ '^omcp_[A-Za-z0-9_-]{8}$'),
    -- HMAC-SHA256 (hex) of the whole key, keyed with API_KEY_PEPPER.
    digest       text        NOT NULL CONSTRAINT api_keys_digest_key UNIQUE CHECK (digest ~ '^[0-9a-f]{64}$'),
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Keys always expire.
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    -- Updated at most once a minute.
    last_used_at timestamptz,
    CONSTRAINT api_keys_expiry CHECK (expires_at > created_at)
);
CREATE INDEX api_keys_user_idx ON api_keys (user_id, created_at DESC);

CREATE TABLE signing_keys (
    kid                text        PRIMARY KEY CHECK (kid ~ '^[A-Za-z0-9_-]{1,100}$'),
    alg                text        NOT NULL DEFAULT 'ES256' CHECK (alg IN ('ES256')),
    -- The PKCS #8 / SEC 1 private key, sealed with ENCRYPTION_KEY (bound to
    -- the kid); cleared when the key is retired.
    private_key_cipher bytea,
    -- The public key as a JWK (published while active or retiring).
    public_jwk         jsonb       NOT NULL,
    state              text        NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'retiring', 'retired')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    -- When it stopped signing, and until when it stays published.
    rotated_at         timestamptz,
    retire_after       timestamptz,
    retired_at         timestamptz,
    CONSTRAINT signing_keys_private CHECK (state = 'retired' OR private_key_cipher IS NOT NULL),
    CONSTRAINT signing_keys_retiring CHECK (state <> 'retiring' OR retire_after IS NOT NULL)
);
-- At most one key signs at a time.
CREATE UNIQUE INDEX signing_keys_one_active ON signing_keys ((true)) WHERE state = 'active';

CREATE TABLE mcp_calls (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at    timestamptz NOT NULL DEFAULT now(),
    request_id     text        NOT NULL DEFAULT '',
    method         text        NOT NULL CHECK (method IN ('tools/list', 'tools/call', 'prompts/list', 'prompts/get')),
    -- The tool or prompt name the client used (calls only).
    exposed_name   text        CHECK (char_length(exposed_name) <= 1100),
    -- The gateway as it was; it may be deleted later (no foreign key).
    gateway_id     uuid        NOT NULL,
    gateway_slug   text        NOT NULL,
    gateway_name   text        NOT NULL,
    -- A team gateway's team.
    gateway_team_id uuid,
    -- The server the call went (or would have gone) to.
    server_id      uuid,
    server_slug    text,
    server_name    text,
    server_team_id uuid,
    capability_id  uuid,
    principal_kind text        NOT NULL DEFAULT 'api_key' CHECK (principal_kind IN ('api_key')),
    user_id        uuid        NOT NULL REFERENCES users (id),
    api_key_id     uuid,
    -- clientInfo.name, when the request carried it.
    client_name    text        CHECK (char_length(client_name) <= 200),
    outcome        text        NOT NULL CHECK (outcome IN ('ok', 'tool_error', 'denied', 'upstream_error', 'timeout',
                                                           'too_large', 'bad_request')),
    error_class    text        CHECK (char_length(error_class) <= 64),
    latency_ms     integer     NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    request_bytes  bigint      NOT NULL DEFAULT 0 CHECK (request_bytes >= 0),
    response_bytes bigint      NOT NULL DEFAULT 0 CHECK (response_bytes >= 0)
);
CREATE INDEX mcp_calls_occurred_idx ON mcp_calls (occurred_at DESC);
CREATE INDEX mcp_calls_gateway_idx ON mcp_calls (gateway_id, id DESC);
CREATE INDEX mcp_calls_server_idx ON mcp_calls (server_id, id DESC) WHERE server_id IS NOT NULL;
CREATE INDEX mcp_calls_user_idx ON mcp_calls (user_id, id DESC);
CREATE INDEX mcp_calls_gateway_team_idx ON mcp_calls (gateway_team_id, id DESC) WHERE gateway_team_id IS NOT NULL;
CREATE INDEX mcp_calls_server_team_idx ON mcp_calls (server_team_id, id DESC) WHERE server_team_id IS NOT NULL;

-- Call records are append-only, like the audit log. Deletes are only
-- allowed for a retention job (Phase 6), which sets omg.calls_purge = 'on'.
-- +goose StatementBegin
CREATE FUNCTION mcp_calls_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('omg.calls_purge', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'mcp_calls is append-only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER mcp_calls_append_only
    BEFORE UPDATE OR DELETE ON mcp_calls
    FOR EACH ROW EXECUTE FUNCTION mcp_calls_append_only();

-- +goose Down
DROP TABLE mcp_calls;
DROP FUNCTION mcp_calls_append_only();
DROP TABLE signing_keys;
DROP TABLE api_keys;
DROP TABLE gateway_grants;
DROP TABLE gateway_capabilities;
DROP TABLE gateways;
ALTER TABLE users DROP COLUMN groups;
