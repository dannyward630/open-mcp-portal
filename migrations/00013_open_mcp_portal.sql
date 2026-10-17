-- The product is now Open MCP Portal (formerly Open MCP Gateway). Names
-- stored in the database follow:
--
--   * The append-only triggers of audit_log (00001) and mcp_calls (00004)
--     allow DELETE when the retention transaction sets omp.audit_purge /
--     omp.calls_purge = 'on'. The old settings omg.audit_purge /
--     omg.calls_purge are still accepted so a previous release's retention
--     loop keeps working during a rolling update; a later release drops them.
--   * api_keys.prefix and service_account_keys.prefix: new keys start with
--     omp_ / omp_sa_. Keys made before (omcp_ / omcp_sa_) stay valid rows so
--     their history is kept, but no longer authenticate.
--
-- Expand-only (expand/contract): the previous release keeps working.

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND (current_setting('omp.audit_purge', true) = 'on'
                             OR current_setting('omg.audit_purge', true) = 'on') THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION mcp_calls_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND (current_setting('omp.calls_purge', true) = 'on'
                             OR current_setting('omg.calls_purge', true) = 'on') THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'mcp_calls is append-only';
END;
$$;
-- +goose StatementEnd

ALTER TABLE api_keys DROP CONSTRAINT api_keys_prefix_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_prefix_check
    CHECK (prefix ~ '^(omp|omcp)_[A-Za-z0-9_-]{8}$');

ALTER TABLE service_account_keys DROP CONSTRAINT service_account_keys_prefix_check;
ALTER TABLE service_account_keys ADD CONSTRAINT service_account_keys_prefix_check
    CHECK (prefix ~ '^(omp|omcp)_sa_[A-Za-z0-9_-]{8}$');

-- +goose Down
-- Keys made since don't satisfy the old checks: NOT VALID keeps them (and
-- their history); the previous release doesn't accept them.
ALTER TABLE service_account_keys DROP CONSTRAINT service_account_keys_prefix_check;
ALTER TABLE service_account_keys ADD CONSTRAINT service_account_keys_prefix_check
    CHECK (prefix ~ '^omcp_sa_[A-Za-z0-9_-]{8}$') NOT VALID;

ALTER TABLE api_keys DROP CONSTRAINT api_keys_prefix_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_prefix_check
    CHECK (prefix ~ '^omcp_[A-Za-z0-9_-]{8}$') NOT VALID;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION mcp_calls_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('omg.calls_purge', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'mcp_calls is append-only';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('omg.audit_purge', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd
