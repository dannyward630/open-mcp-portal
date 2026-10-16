-- Pre-published signing keys (docs/upstream-identity.md): a key is created
-- in state 'next' at least SIGNING_KEY_PREPUBLISH before it starts signing.
-- It is published in the JWKS but never signs until the rotation loop
-- promotes it to 'active' (and the old active key becomes 'retiring').
--
-- activated_at is when a key started signing; its rotation time counts from
-- then (keys activated before this migration count from created_at).
--
-- Widens a CHECK constraint, adds a nullable column and an index only: the
-- previous release keeps working (it never reads or writes 'next' keys; it
-- would publish none and sign with the active key as before).

-- +goose Up
ALTER TABLE signing_keys ADD COLUMN activated_at timestamptz;
ALTER TABLE signing_keys DROP CONSTRAINT signing_keys_state_check;
ALTER TABLE signing_keys ADD CONSTRAINT signing_keys_state_check
    CHECK (state IN ('next', 'active', 'retiring', 'retired'));
-- At most one key waits to sign.
CREATE UNIQUE INDEX signing_keys_one_next ON signing_keys ((true)) WHERE state = 'next';

-- +goose Down
DROP INDEX signing_keys_one_next;
DELETE FROM signing_keys WHERE state = 'next';
ALTER TABLE signing_keys DROP CONSTRAINT signing_keys_state_check;
ALTER TABLE signing_keys ADD CONSTRAINT signing_keys_state_check
    CHECK (state IN ('active', 'retiring', 'retired'));
ALTER TABLE signing_keys DROP COLUMN activated_at;
