-- Automation tokens (docs/automation-tokens.md; decisions 55 and 58): bearer
-- credentials for CI and scripts that call /v1, never /mcp.
--
--   * automation_tokens: platform-admin tokens (ompadm_, scopes platform:read
--     and platform:write) and team publisher tokens (omppub_, scopes
--     servers:read and servers:write, owned by one team). Only the HMAC-SHA256
--     digest under API_KEY_PEPPER is stored (pepper_id says which pepper),
--     with the first 15 characters of the token (the prefix) to recognise it.
--     Tokens always expire (at most 365 days). A platform token is refused
--     while its creator isn't an active platform admin; a team token outlives
--     its creator (created_by is SET NULL, created_by_name stays).
--   * audit_log: actor_kind gains automation_token, with the token's id,
--     name and kind as they were when it acted (no foreign key: the log
--     outlives the token).
--
-- New table, new nullable columns and a relaxed check (expand/contract): the
-- previous release keeps working, except that it can't list audit entries of
-- the new actor kind, so create no token until every replica runs this one.

-- +goose Up
CREATE TABLE automation_tokens (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text        NOT NULL CHECK (kind IN ('platform_admin', 'team_publisher')),
    team_id         uuid        REFERENCES teams (id) ON DELETE CASCADE,
    name            text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    prefix          text        NOT NULL CHECK (prefix ~ '^(ompadm|omppub)_[A-Za-z0-9_-]{8}$'),
    digest          text        NOT NULL CONSTRAINT automation_tokens_digest_key UNIQUE CHECK (digest ~ '^[0-9a-f]{64}$'),
    pepper_id       bytea       CHECK (octet_length(pepper_id) = 8),
    scopes          text[]      NOT NULL CHECK (cardinality(scopes) >= 1
                                                AND scopes <@ ARRAY['platform:read', 'platform:write', 'servers:read', 'servers:write']::text[]),
    created_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_by_name text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    revoked_at      timestamptz,
    revoked_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    last_used_at    timestamptz,
    CONSTRAINT automation_tokens_expiry CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days'),
    CONSTRAINT automation_tokens_team CHECK ((kind = 'team_publisher') = (team_id IS NOT NULL)),
    CONSTRAINT automation_tokens_prefix_kind CHECK ((kind = 'platform_admin' AND prefix LIKE 'ompadm\_%')
                                                 OR (kind = 'team_publisher' AND prefix LIKE 'omppub\_%')),
    CONSTRAINT automation_tokens_scopes_kind CHECK ((kind = 'platform_admin' AND scopes <@ ARRAY['platform:read', 'platform:write']::text[])
                                                 OR (kind = 'team_publisher' AND scopes <@ ARRAY['servers:read', 'servers:write']::text[]))
);
CREATE INDEX automation_tokens_team_idx ON automation_tokens (team_id, created_at DESC) WHERE team_id IS NOT NULL;
CREATE INDEX automation_tokens_kind_idx ON automation_tokens (kind, created_at DESC);
CREATE INDEX automation_tokens_expires_idx ON automation_tokens (expires_at);

ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'system', 'automation_token'));
ALTER TABLE audit_log ADD COLUMN actor_token_id   uuid;
ALTER TABLE audit_log ADD COLUMN actor_token_name text;
ALTER TABLE audit_log ADD COLUMN actor_token_kind text;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_token CHECK (
    (actor_kind = 'automation_token') = (actor_token_id IS NOT NULL)
    AND (actor_token_id IS NULL) = (actor_token_name IS NULL)
    AND (actor_token_id IS NULL) = (actor_token_kind IS NULL)
    AND (actor_token_kind IS NULL OR actor_token_kind IN ('platform_admin', 'team_publisher'))
) NOT VALID;
ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_actor_token;
CREATE INDEX audit_log_actor_token_idx ON audit_log (actor_token_id, occurred_at DESC) WHERE actor_token_id IS NOT NULL;

-- +goose Down
DROP INDEX audit_log_actor_token_idx;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_token;
ALTER TABLE audit_log DROP COLUMN actor_token_kind;
ALTER TABLE audit_log DROP COLUMN actor_token_name;
ALTER TABLE audit_log DROP COLUMN actor_token_id;
-- Entries of the new actor kind may exist: NOT VALID keeps them.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'system')) NOT VALID;
DROP TABLE automation_tokens;
