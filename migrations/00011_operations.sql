-- Phase 6: operations (docs/phase6-operations.md; decisions 31-38):
--
--   * limit_settings: one row; the platform admins' rate limit defaults and
--     ceilings, a JSON object keyed by limit ({"default": n|null,
--     "ceiling": n|null}); absent keys use the built-in defaults
--     (internal/limits).
--   * capture_settings: one row; the platform admins' extra redaction
--     patterns, a JSON array of {pattern, note?} (internal/capture).
--   * retention_settings: one row; the platform periods in days per kind (a
--     JSON object; absent kinds use the built-in periods) and the last run of
--     the retention loop (internal/retention).
--   * teams.limit_overrides, teams.retention_overrides: a team's own values
--     (JSON objects; {} inherits everything). Edits bump the team's revision.
--   * gateways.rate_limit_per_minute: the gateway's own limit (NULL:
--     inherit); gateways.capture_payloads (since 00004) becomes live.
--   * mcp_servers.capture_payloads: the server's team records call contents.
--   * mcp_calls: outcome rate_limited. The append-only trigger (00004) already
--     allows DELETE only with omg.calls_purge = 'on' (the retention loop).
--   * mcp_call_payloads: captured arguments and results, redacted, truncated
--     and sealed with ENCRYPTION_KEY (AAD mcp_call_payload:<call id>:<part>).
--   * rate_limit_counters: fixed one-minute windows shared by every replica.
--     UNLOGGED: counters may be lost on a crash or failover, by design.
--   * usage_hourly: hourly rollups of mcp_calls (internal/usage);
--     usage_rollup_state: how far they are complete.
--   * api_keys, service_account_keys, oauth_tokens .pepper_id: the first 8
--     bytes of SHA-256 of the pepper that made the digest (NULL: made before
--     this release; stamped at start-up).
--
-- New tables, defaulted or nullable columns and a relaxed check
-- (expand/contract): the previous release keeps working, except that its
-- call log can't show the new outcome.

-- +goose Up
CREATE TABLE limit_settings (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    items      jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(items) = 'object'),
    revision   bigint      NOT NULL DEFAULT 1,
    updated_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO limit_settings (id) VALUES (1);

CREATE TABLE capture_settings (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    patterns   jsonb       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(patterns) = 'array'),
    revision   bigint      NOT NULL DEFAULT 1,
    updated_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO capture_settings (id) VALUES (1);

CREATE TABLE retention_settings (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    periods    jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(periods) = 'object'),
    revision   bigint      NOT NULL DEFAULT 1,
    updated_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- {startedAt, finishedAt, deleted: {kind: n}, errors: {kind: class}}
    last_run   jsonb
);
INSERT INTO retention_settings (id) VALUES (1);

ALTER TABLE teams
    ADD COLUMN limit_overrides     jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(limit_overrides) = 'object'),
    ADD COLUMN retention_overrides jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retention_overrides) = 'object');

ALTER TABLE gateways ADD COLUMN rate_limit_per_minute bigint CHECK (rate_limit_per_minute BETWEEN 1 AND 1000000);

ALTER TABLE mcp_servers ADD COLUMN capture_payloads boolean NOT NULL DEFAULT false;

ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_outcome_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_outcome_check
    CHECK (outcome IN ('ok', 'tool_error', 'denied', 'upstream_error', 'timeout', 'too_large', 'bad_request',
                       'needs_authorization', 'rate_limited'));

CREATE TABLE mcp_call_payloads (
    call_id              bigint      PRIMARY KEY REFERENCES mcp_calls (id) ON DELETE CASCADE,
    occurred_at          timestamptz NOT NULL,
    gateway_team_id      uuid,
    server_team_id       uuid,
    -- Whose switch captured it (at least one).
    by_gateway           boolean     NOT NULL,
    by_server            boolean     NOT NULL,
    arguments_cipher     bytea       NOT NULL,
    result_cipher        bytea,
    -- Sizes of the redacted JSON before truncation.
    arguments_bytes      bigint      NOT NULL CHECK (arguments_bytes >= 0),
    result_bytes         bigint      NOT NULL DEFAULT 0 CHECK (result_bytes >= 0),
    arguments_truncated  boolean     NOT NULL DEFAULT false,
    result_truncated     boolean     NOT NULL DEFAULT false,
    arguments_redactions integer     NOT NULL DEFAULT 0 CHECK (arguments_redactions >= 0),
    result_redactions    integer     NOT NULL DEFAULT 0 CHECK (result_redactions >= 0),
    captured_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mcp_call_payloads_captured_by CHECK (by_gateway OR by_server)
);
CREATE INDEX mcp_call_payloads_captured_idx ON mcp_call_payloads (captured_at);
CREATE INDEX mcp_call_payloads_gateway_team_idx ON mcp_call_payloads (gateway_team_id) WHERE gateway_team_id IS NOT NULL;
CREATE INDEX mcp_call_payloads_server_team_idx ON mcp_call_payloads (server_team_id) WHERE server_team_id IS NOT NULL;

CREATE UNLOGGED TABLE rate_limit_counters (
    bucket       text        NOT NULL CHECK (char_length(bucket) <= 200),
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL DEFAULT 1,
    PRIMARY KEY (bucket, window_start)
);
CREATE INDEX rate_limit_counters_window_idx ON rate_limit_counters (window_start);

CREATE TABLE usage_hourly (
    id                      bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    hour                    timestamptz NOT NULL,
    gateway_id              uuid        NOT NULL,
    gateway_slug            text        NOT NULL,
    gateway_name            text        NOT NULL,
    gateway_team_id         uuid,
    server_id               uuid,
    server_slug             text,
    server_name             text,
    server_team_id          uuid,
    capability_id           uuid,
    exposed_name            text,
    method                  text        NOT NULL,
    principal_kind          text        NOT NULL,
    service_account_team_id uuid,
    outcome                 text        NOT NULL,
    calls                   bigint      NOT NULL,
    latency_sum_ms          bigint      NOT NULL,
    -- Counts per fixed latency bucket (internal/usage.LatencyBuckets, then +Inf).
    latency_buckets         bigint[]    NOT NULL,
    request_bytes           bigint      NOT NULL,
    response_bytes          bigint      NOT NULL,
    CONSTRAINT usage_hourly_key UNIQUE NULLS NOT DISTINCT (hour, gateway_id, gateway_team_id, server_id, server_team_id,
        capability_id, exposed_name, method, principal_kind, service_account_team_id, outcome)
);
CREATE INDEX usage_hourly_hour_idx ON usage_hourly (hour);
CREATE INDEX usage_hourly_gateway_team_idx ON usage_hourly (gateway_team_id, hour) WHERE gateway_team_id IS NOT NULL;
CREATE INDEX usage_hourly_server_team_idx ON usage_hourly (server_team_id, hour) WHERE server_team_id IS NOT NULL;

CREATE TABLE usage_rollup_state (
    id           smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    -- Calls recorded before this time are in usage_hourly.
    rolled_up_to timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
INSERT INTO usage_rollup_state (id) VALUES (1);

ALTER TABLE api_keys ADD COLUMN pepper_id bytea CHECK (octet_length(pepper_id) = 8);
ALTER TABLE service_account_keys ADD COLUMN pepper_id bytea CHECK (octet_length(pepper_id) = 8);
ALTER TABLE oauth_tokens ADD COLUMN pepper_id bytea CHECK (octet_length(pepper_id) = 8);

-- +goose Down
ALTER TABLE oauth_tokens DROP COLUMN pepper_id;
ALTER TABLE service_account_keys DROP COLUMN pepper_id;
ALTER TABLE api_keys DROP COLUMN pepper_id;
DROP TABLE usage_rollup_state;
DROP TABLE usage_hourly;
DROP TABLE rate_limit_counters;
DROP TABLE mcp_call_payloads;
-- Call records are append-only: rows with the new outcome are kept, and
-- only new rows are checked.
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_outcome_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_outcome_check
    CHECK (outcome IN ('ok', 'tool_error', 'denied', 'upstream_error', 'timeout', 'too_large', 'bad_request',
                       'needs_authorization')) NOT VALID;
ALTER TABLE mcp_servers DROP COLUMN capture_payloads;
ALTER TABLE gateways DROP COLUMN rate_limit_per_minute;
ALTER TABLE teams DROP COLUMN limit_overrides, DROP COLUMN retention_overrides;
DROP TABLE retention_settings;
DROP TABLE capture_settings;
DROP TABLE limit_settings;
