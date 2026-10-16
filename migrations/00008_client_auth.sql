-- Phase 4: client sign-in (OAuth) and service accounts
-- (docs/phase4-client-auth.md; decisions 12, 21-24):
--
--   * oauth_settings: one row; the trusted-client allowlist (decision 24), a
--     JSON array of {clientId, label?} (https Client ID Metadata Document
--     URLs only), with its own revision. Seeded with Claude, Claude Code and
--     VS Code.
--   * oauth_clients: clients registered with Dynamic Client Registration
--     (RFC 7591). Clients identified by a metadata document aren't stored:
--     their document is fetched and cached in memory. A registration unused
--     for 30 days is deleted.
--   * oauth_grants: a person's consent for a client on one gateway
--     (decision 21: one connection per gateway); at most one active grant per
--     person, client and gateway. Revoking it ends its codes and tokens.
--     Deleting the gateway deletes its grants.
--   * oauth_codes: authorization codes (HMAC digests), single use, 60 s.
--   * oauth_tokens: access and refresh tokens as HMAC-SHA256 digests under
--     API_KEY_PEPPER, never in clear.
--   * service_accounts: team-owned identities (role member or editor,
--     decision 22), names unique per team.
--   * service_account_keys: their keys (HMAC digests, always expire), and
--     service_account_key_gateways: the gateways a key is limited to (no
--     foreign key on the gateway, like api_key_gateways).
--   * mcp_calls: principal kinds oauth and service_account. user_id is NULL
--     for service accounts; service_account_* and oauth_* hold who called as
--     they were (no foreign keys: records outlive grants and accounts), and
--     key_name the key's name as it was. api_key_id holds the key used: an
--     API key (api_key) or a service account key (service_account).
--
-- New tables and a relaxed column (expand/contract): the previous release
-- keeps working, except that its call log can't read rows of service
-- accounts (user_id NULL) written once this release runs.

-- +goose Up
CREATE TABLE oauth_settings (
    id              smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    trusted_clients jsonb       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(trusted_clients) = 'array'),
    revision        bigint      NOT NULL DEFAULT 1,
    updated_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
INSERT INTO oauth_settings (id, trusted_clients) VALUES (1, '[
  {"clientId": "https://claude.ai/oauth/mcp-oauth-client-metadata", "label": "Claude"},
  {"clientId": "https://claude.ai/oauth/claude-code-client-metadata", "label": "Claude Code"},
  {"clientId": "https://vscode.dev/oauth/client-metadata.json", "label": "VS Code"}
]'::jsonb);

CREATE TABLE oauth_clients (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id     text        NOT NULL CONSTRAINT oauth_clients_client_id_key UNIQUE CHECK (char_length(client_id) BETWEEN 1 AND 100),
    client_name   text        NOT NULL CHECK (char_length(client_name) BETWEEN 1 AND 200),
    redirect_uris text[]      NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 10),
    client_uri    text        CHECK (char_length(client_uri) <= 500),
    logo_uri      text        CHECK (char_length(logo_uri) <= 500),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_clients_last_used_idx ON oauth_clients (last_used_at);

CREATE TABLE oauth_grants (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    gateway_id     uuid        NOT NULL REFERENCES gateways (id) ON DELETE CASCADE,
    -- The client as it identified itself: an https URL (a metadata
    -- document) or a registered client's id.
    client_id      text        NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 500),
    client_kind    text        NOT NULL CHECK (client_kind IN ('metadata', 'registered')),
    -- The client as consent was given.
    client_name    text        NOT NULL CHECK (char_length(client_name) BETWEEN 1 AND 200),
    client_uri     text        CHECK (char_length(client_uri) <= 500),
    logo_uri       text        CHECK (char_length(logo_uri) <= 500),
    -- The protected resource: <APP_URL>/mcp/{slug} of the gateway.
    resource       text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- Updated at most once a minute.
    last_used_at   timestamptz,
    revoked_at     timestamptz,
    revoked_reason text        CHECK (revoked_reason IN ('user', 'admin', 'client', 'refresh_reuse'))
);
CREATE UNIQUE INDEX oauth_grants_active_key ON oauth_grants (user_id, client_id, gateway_id) WHERE revoked_at IS NULL;
CREATE INDEX oauth_grants_user_idx ON oauth_grants (user_id, created_at DESC);
CREATE INDEX oauth_grants_gateway_idx ON oauth_grants (gateway_id);

CREATE TABLE oauth_codes (
    code_hash      text        PRIMARY KEY CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    grant_id       uuid        NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
    client_id      text        NOT NULL,
    redirect_uri   text        NOT NULL,
    code_challenge text        NOT NULL,
    resource       text        NOT NULL,
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_codes_expires_idx ON oauth_codes (expires_at);
CREATE INDEX oauth_codes_grant_idx ON oauth_codes (grant_id);

CREATE TABLE oauth_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    grant_id   uuid        NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('access', 'refresh')),
    -- HMAC-SHA256 (hex) of "oauth:" and the token, keyed with API_KEY_PEPPER.
    token_hash text        NOT NULL CONSTRAINT oauth_tokens_token_hash_key UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    resource   text        NOT NULL,
    expires_at timestamptz NOT NULL,
    -- A refresh token exchanged for a new pair: presenting it again later
    -- than the grace period is reuse, which revokes the grant.
    rotated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_tokens_grant_idx ON oauth_tokens (grant_id);
CREATE INDEX oauth_tokens_expires_idx ON oauth_tokens (expires_at);

CREATE TABLE service_accounts (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id      uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (name ~ '^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$'),
    description  text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    -- The team role used for capabilities' minimum roles; never owner or admin.
    role         text        NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'editor')),
    enabled      boolean     NOT NULL DEFAULT true,
    revision     bigint      NOT NULL DEFAULT 1,
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- Updated at most once a minute.
    last_used_at timestamptz,
    CONSTRAINT service_accounts_team_name_key UNIQUE (team_id, name)
);

CREATE TABLE service_account_keys (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_account_id uuid        NOT NULL REFERENCES service_accounts (id) ON DELETE CASCADE,
    name               text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- The key's first characters: omcp_sa_ and 8 more.
    prefix             text        NOT NULL CHECK (prefix ~ '^omcp_sa_[A-Za-z0-9_-]{8}$'),
    -- HMAC-SHA256 (hex) of the whole key, keyed with API_KEY_PEPPER.
    digest             text        NOT NULL CONSTRAINT service_account_keys_digest_key UNIQUE CHECK (digest ~ '^[0-9a-f]{64}$'),
    created_by         uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    revoked_at         timestamptz,
    last_used_at       timestamptz,
    CONSTRAINT service_account_keys_expiry CHECK (expires_at > created_at)
);
CREATE INDEX service_account_keys_account_idx ON service_account_keys (service_account_id, created_at DESC);

CREATE TABLE service_account_key_gateways (
    key_id       uuid        NOT NULL REFERENCES service_account_keys (id) ON DELETE CASCADE,
    gateway_id   uuid        NOT NULL,
    gateway_slug text        NOT NULL,
    gateway_name text        NOT NULL,
    added_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key_id, gateway_id)
);

ALTER TABLE mcp_calls ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_principal_kind_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_principal_kind_check
    CHECK (principal_kind IN ('api_key', 'oauth', 'service_account'));
ALTER TABLE mcp_calls
    ADD COLUMN service_account_id      uuid,
    ADD COLUMN service_account_name    text CHECK (char_length(service_account_name) <= 64),
    ADD COLUMN service_account_team_id uuid,
    ADD COLUMN oauth_grant_id          uuid,
    ADD COLUMN oauth_client_name       text CHECK (char_length(oauth_client_name) <= 200),
    ADD COLUMN key_name                text CHECK (char_length(key_name) <= 100);
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_principal CHECK (
    (principal_kind = 'service_account') = (service_account_id IS NOT NULL)
    AND (principal_kind = 'service_account' OR user_id IS NOT NULL)
    AND (principal_kind = 'oauth') = (oauth_grant_id IS NOT NULL));
CREATE INDEX mcp_calls_service_account_idx ON mcp_calls (service_account_id, id DESC) WHERE service_account_id IS NOT NULL;

-- +goose Down
DROP INDEX mcp_calls_service_account_idx;
-- Call records are append-only: rows of OAuth clients and service accounts
-- are kept, and only new rows are checked.
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_principal;
ALTER TABLE mcp_calls
    DROP COLUMN service_account_id,
    DROP COLUMN service_account_name,
    DROP COLUMN service_account_team_id,
    DROP COLUMN oauth_grant_id,
    DROP COLUMN oauth_client_name,
    DROP COLUMN key_name;
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_principal_kind_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_principal_kind_check CHECK (principal_kind IN ('api_key')) NOT VALID;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_user_id_not_null CHECK (user_id IS NOT NULL) NOT VALID;
DROP TABLE service_account_key_gateways;
DROP TABLE service_account_keys;
DROP TABLE service_accounts;
DROP TABLE oauth_tokens;
DROP TABLE oauth_codes;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
DROP TABLE oauth_settings;
