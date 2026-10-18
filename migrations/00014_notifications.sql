-- Notifications (docs/notifications.md; decision 56): the events the portal's
-- own changes and loops emit, and their delivery to people (in-app inbox,
-- email) and to webhook destinations.
--
--   * notification_events: one row per event, written in the transaction of
--     the change (internal/notify). dedup_key makes emission idempotent; the
--     payload is the event's `data` only (names, counts, times: never
--     secrets, call contents or addresses). fanned_out_at is NULL until the
--     dispatcher has resolved recipients. Kept 14 days.
--   * notification_inbox: a person's copy (title, body and link are composed
--     when the event is fanned out, so the row outlives the event). Read
--     state is per person. Kept 90 days. event_id has no foreign key for the
--     same reason.
--   * notification_outbox: the deliveries still to do or done, email (by
--     person; the address is read from the profile at send time) and
--     webhook (by destination). state pending, sent, failed or skipped;
--     dedup_key makes fan-out idempotent. Kept 14 days.
--   * notification_preferences: per person, event and channel, only rows that
--     differ from the defaults (in-app on, email on).
--   * notification_destinations: webhooks. The URL and the signing secrets
--     are sealed with ENCRYPTION_KEY (AAD notification_destination:<id>:url,
--     :secret and :secret_previous; keyrotation.Columns lists them) and never
--     returned: url_display is scheme and host only.
--   * notification_state: one row, the dispatcher's heartbeat.
--   * users.email_verified: whether the provider vouched for the address at
--     the person's last sign-in. Email goes only to verified addresses (with
--     OIDC_REQUIRE_VERIFIED_EMAIL=false an attacker-chosen address could
--     otherwise receive a team's notifications). false until the next sign-in.
--
-- New tables only (expand/contract): the previous release keeps working.

-- +goose Up
ALTER TABLE users ADD COLUMN email_verified boolean NOT NULL DEFAULT false;

CREATE TABLE notification_events (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id         uuid        NOT NULL DEFAULT gen_random_uuid() CONSTRAINT notification_events_public_id_key UNIQUE,
    event             text        NOT NULL CHECK (event IN ('server.unhealthy', 'server.recovered', 'capability.changed', 'publish.requested',
                                                            'publish.decided', 'connection.needs_reconsent', 'upstream_signin.lapsed', 'key.expiring')),
    dedup_key         text        NOT NULL CONSTRAINT notification_events_dedup_key_key UNIQUE CHECK (char_length(dedup_key) <= 200),
    team_id           uuid        REFERENCES teams (id) ON DELETE CASCADE,
    subject_type      text        NOT NULL DEFAULT '' CHECK (char_length(subject_type) <= 64),
    subject_id        text        NOT NULL DEFAULT '' CHECK (char_length(subject_id) <= 100),
    -- Personal events (connection.needs_reconsent, personal key.expiring).
    recipient_user_id uuid        REFERENCES users (id) ON DELETE CASCADE,
    -- The person who caused a manual event: they are not told about it.
    actor_user_id     uuid        REFERENCES users (id) ON DELETE SET NULL,
    payload           jsonb       NOT NULL CHECK (octet_length(payload::text) <= 8192),
    occurred_at       timestamptz NOT NULL DEFAULT now(),
    fanned_out_at     timestamptz
);
CREATE INDEX notification_events_pending_idx ON notification_events (id) WHERE fanned_out_at IS NULL;
CREATE INDEX notification_events_occurred_idx ON notification_events (occurred_at);
-- Looking up an incident's events (server.recovered needs the unhealthy one).
CREATE INDEX notification_events_subject_idx ON notification_events (event, subject_id, id DESC);

CREATE TABLE notification_inbox (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_id   bigint      NOT NULL,
    event      text        NOT NULL,
    team_id    uuid,
    title      text        NOT NULL CHECK (char_length(title) <= 200),
    body       text        NOT NULL CHECK (char_length(body) <= 500),
    link       jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at    timestamptz,
    CONSTRAINT notification_inbox_event_key UNIQUE (user_id, event_id)
);
CREATE INDEX notification_inbox_user_idx ON notification_inbox (user_id, id DESC);
CREATE INDEX notification_inbox_unread_idx ON notification_inbox (user_id, id DESC) WHERE read_at IS NULL;
CREATE INDEX notification_inbox_created_idx ON notification_inbox (created_at);

CREATE TABLE notification_preferences (
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event      text        NOT NULL CHECK (event IN ('server.unhealthy', 'server.recovered', 'capability.changed', 'publish.requested',
                                                     'publish.decided', 'connection.needs_reconsent', 'upstream_signin.lapsed', 'key.expiring')),
    channel    text        NOT NULL CHECK (channel IN ('in_app', 'email')),
    enabled    boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, event, channel)
);

CREATE TABLE notification_destinations (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope                 text        NOT NULL CHECK (scope IN ('team', 'platform')),
    team_id               uuid        REFERENCES teams (id) ON DELETE CASCADE,
    name                  text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    format                text        NOT NULL CHECK (format IN ('json', 'slack')),
    url_cipher            bytea       NOT NULL,
    url_display           text        NOT NULL CHECK (char_length(url_display) <= 300),
    -- json destinations only.
    secret_cipher         bytea,
    secret_previous_cipher bytea,
    secret_previous_until timestamptz,
    secret_hint           text        NOT NULL DEFAULT '' CHECK (char_length(secret_hint) <= 8),
    secret_rotated_at     timestamptz,
    events                text[]      NOT NULL CHECK (cardinality(events) BETWEEN 1 AND 8),
    team_events           boolean     NOT NULL DEFAULT false,
    enabled               boolean     NOT NULL DEFAULT true,
    disabled_reason       text        CHECK (disabled_reason IN ('failures')),
    consecutive_failures  integer     NOT NULL DEFAULT 0,
    last_success_at       timestamptz,
    last_failure_at       timestamptz,
    last_error_class      text,
    revision              bigint      NOT NULL DEFAULT 1,
    created_by            uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_by_name       text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_by            uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_destinations_scope CHECK ((scope = 'team') = (team_id IS NOT NULL)),
    CONSTRAINT notification_destinations_team_events CHECK (scope = 'platform' OR NOT team_events),
    CONSTRAINT notification_destinations_secret CHECK ((format = 'json') = (secret_cipher IS NOT NULL))
);
-- Names are unique within a team, and among platform destinations (case-insensitively).
CREATE UNIQUE INDEX notification_destinations_name_key
    ON notification_destinations (COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));
CREATE INDEX notification_destinations_team_idx ON notification_destinations (team_id) WHERE team_id IS NOT NULL;
CREATE INDEX notification_destinations_scope_idx ON notification_destinations (scope, created_at);

CREATE TABLE notification_outbox (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id        bigint      NOT NULL REFERENCES notification_events (id) ON DELETE CASCADE,
    channel         text        NOT NULL CHECK (channel IN ('email', 'webhook')),
    user_id         uuid        REFERENCES users (id) ON DELETE CASCADE,
    destination_id  uuid        REFERENCES notification_destinations (id) ON DELETE CASCADE,
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sent', 'failed', 'skipped')),
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_attempt_at timestamptz,
    sent_at         timestamptz,
    http_status     integer,
    error_class     text,
    dedup_key       text        NOT NULL CONSTRAINT notification_outbox_dedup_key_key UNIQUE CHECK (char_length(dedup_key) <= 240),
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_outbox_target CHECK ((channel = 'email' AND user_id IS NOT NULL AND destination_id IS NULL)
                                              OR (channel = 'webhook' AND destination_id IS NOT NULL AND user_id IS NULL))
);
CREATE INDEX notification_outbox_due_idx ON notification_outbox (next_attempt_at) WHERE state = 'pending';
CREATE INDEX notification_outbox_destination_idx ON notification_outbox (destination_id, id DESC) WHERE destination_id IS NOT NULL;
CREATE INDEX notification_outbox_user_idx ON notification_outbox (user_id, created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX notification_outbox_created_idx ON notification_outbox (created_at);

CREATE TABLE notification_state (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_run_at timestamptz,
    key_scan_at timestamptz
);
INSERT INTO notification_state (id) VALUES (1);

-- +goose Down
ALTER TABLE users DROP COLUMN email_verified;
DROP TABLE notification_state;
DROP TABLE notification_outbox;
DROP TABLE notification_destinations;
DROP TABLE notification_preferences;
DROP TABLE notification_inbox;
DROP TABLE notification_events;
