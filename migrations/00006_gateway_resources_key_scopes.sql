-- Phase 3 follow-ups (docs/phase3-gateways.md, "As built"):
--
--   * mcp_calls.method also accepts resources/list, resources/templates/list
--     and resources/read, now that gateways serve selected resources and
--     resource templates (exposed_name then holds the exposed URI the client
--     asked for, omcp://{serverSlug}/{originalUri}).
--   * api_key_gateways: the gateways an API key is limited to. No rows: the
--     key works on every gateway its owner may use. gateway_id has no
--     foreign key on purpose: deleting a gateway leaves the entry (shown as
--     a gateway that no longer exists), so the key never silently widens to
--     every gateway. gateway_slug and gateway_name are as they were when the
--     key was created, for such entries.
--
-- Widens a CHECK constraint and adds a table only (expand/contract): the
-- previous release keeps working (it ignores scopes, so scoped keys work on
-- every gateway their owner may use until this release is deployed).

-- +goose Up
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_method_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_method_check
    CHECK (method IN ('tools/list', 'tools/call', 'prompts/list', 'prompts/get',
                      'resources/list', 'resources/templates/list', 'resources/read'));

CREATE TABLE api_key_gateways (
    api_key_id   uuid        NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
    gateway_id   uuid        NOT NULL,
    gateway_slug text        NOT NULL,
    gateway_name text        NOT NULL,
    added_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (api_key_id, gateway_id)
);
CREATE INDEX api_key_gateways_gateway_idx ON api_key_gateways (gateway_id);

-- +goose Down
DROP TABLE api_key_gateways;
-- Call records are append-only: older rows with resource methods are kept
-- and only new rows are checked.
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_method_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_method_check
    CHECK (method IN ('tools/list', 'tools/call', 'prompts/list', 'prompts/get')) NOT VALID;
