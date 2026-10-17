#!/usr/bin/env bash
# One-off: move a development database from the former compose project
# (open-mcp-gateway: container open-mcp-gateway-postgres-1, database and user
# omg) to this one (open-mcp-portal: container open-mcp-portal-postgres-1,
# database and user omp). Decision 46, docs/status.md.
#
# It never stops, removes or changes the old container or its volume
# (open-mcp-gateway_postgres), so the old database stays as a fallback.
#
#   1. Dumps database omg from the old container (pg_dump -Fc, through
#      docker exec) to $DUMP. When the old container isn't running, an
#      existing $DUMP is used instead.
#   2. Starts this project's Postgres (docker compose up) if it isn't running:
#      on 127.0.0.1:55442 when that port is free, else on $TEMP_PORT
#      (OMP_DEV_POSTGRES_PORT, see compose.yaml) until the swap below.
#   3. Restores the dump into database omp in one transaction, objects owned
#      by omp, and compares row counts of a few tables with the old database.
#
# Idempotent: when database omp already holds the schema (goose_db_version),
# it says so and stops; FORCE=1 drops and recreates database omp first (only
# omp, in the new container). A failed restore leaves omp empty (single
# transaction), so it can simply be run again.
#
# Afterwards (printed at the end): stop the dev server, `docker stop
# open-mcp-gateway-postgres-1`, `docker compose up -d --wait` (moves the new
# Postgres to 55442 if it was started on $TEMP_PORT; the data is in the volume
# open-mcp-portal_postgres), point DATABASE_URL in .env at omp, and `make run`
# (MIGRATE_ON_START applies 00013_open_mcp_portal). Keep ENCRYPTION_KEY and
# API_KEY_PEPPER in .env unchanged: stored secrets are sealed with them.
#
# Usage: scripts/migrate-dev-db.sh        (from anywhere in the repository)
#   OLD_CONTAINER (open-mcp-gateway-postgres-1)  OLD_USER/OLD_DB (omg)
#   DUMP (bin/dev-db/omg.dump)  TEMP_PORT (55443)  FORCE (unset)
set -euo pipefail

cd "$(dirname "$0")/.."

OLD_CONTAINER="${OLD_CONTAINER:-open-mcp-gateway-postgres-1}"
OLD_USER="${OLD_USER:-omg}"
OLD_DB="${OLD_DB:-omg}"
NEW_PROJECT="open-mcp-portal"
NEW_CONTAINER="${NEW_PROJECT}-postgres-1"
NEW_USER="omp"
NEW_DB="omp"
PORT=55442
TEMP_PORT="${TEMP_PORT:-55443}"
DUMP="${DUMP:-bin/dev-db/omg.dump}"
TABLES=(users teams team_members mcp_servers gateways auth_providers provider_connections api_keys audit_log)

say() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null || true)" = "true" ]; }
port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
new_psql() { docker exec -i "$NEW_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U "$NEW_USER" "$@"; }
count() { # container user db table
  docker exec "$1" psql -X -Atq -U "$2" -d "$3" -c "SELECT count(*) FROM $4" 2>/dev/null || echo "-"
}

command -v docker >/dev/null || die "docker not found"
[ "$(docker compose config --format json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["name"])' 2>/dev/null || true)" = "$NEW_PROJECT" ] ||
  die "compose.yaml is not the $NEW_PROJECT project; run this from the renamed checkout"

# 1. Dump the old database.
mkdir -p "$(dirname "$DUMP")"
if running "$OLD_CONTAINER"; then
  say "dumping database $OLD_DB from $OLD_CONTAINER to $DUMP"
  docker exec "$OLD_CONTAINER" pg_dump -U "$OLD_USER" -d "$OLD_DB" -Fc --no-owner --no-privileges >"$DUMP.tmp"
  mv "$DUMP.tmp" "$DUMP"
elif [ -s "$DUMP" ]; then
  say "$OLD_CONTAINER is not running; using the existing dump $DUMP ($(date -r "$DUMP" '+%F %T'))"
else
  die "$OLD_CONTAINER is not running and there is no dump at $DUMP; start it with: docker start $OLD_CONTAINER"
fi

# 2. Start this project's Postgres.
if running "$NEW_CONTAINER"; then
  say "$NEW_CONTAINER is already running"
else
  if port_free "$PORT"; then
    say "starting $NEW_CONTAINER on 127.0.0.1:$PORT"
    docker compose up -d --wait postgres
  else
    port_free "$TEMP_PORT" || die "ports $PORT and $TEMP_PORT are both taken; set TEMP_PORT"
    say "127.0.0.1:$PORT is taken (by $OLD_CONTAINER?); starting $NEW_CONTAINER on 127.0.0.1:$TEMP_PORT for now"
    OMP_DEV_POSTGRES_PORT="$TEMP_PORT" docker compose up -d --wait postgres
  fi
fi

# 3. Restore into omp.
restored="$(new_psql -d "$NEW_DB" -Atq -c "SELECT to_regclass('public.goose_db_version') IS NOT NULL")"
if [ "$restored" = "t" ]; then
  if [ "${FORCE:-}" != "1" ]; then
    version="$(new_psql -d "$NEW_DB" -Atq -c "SELECT max(version_id) FROM goose_db_version WHERE is_applied")"
    say "database $NEW_DB in $NEW_CONTAINER already has the schema (migration $version); nothing to do (FORCE=1 replaces it)"
    exit 0
  fi
  say "FORCE=1: dropping and recreating database $NEW_DB in $NEW_CONTAINER"
  new_psql -d postgres -q -c "DROP DATABASE $NEW_DB WITH (FORCE)" -c "CREATE DATABASE $NEW_DB OWNER $NEW_USER"
fi
say "restoring $DUMP into database $NEW_DB (one transaction)"
docker exec -i "$NEW_CONTAINER" pg_restore -U "$NEW_USER" -d "$NEW_DB" --no-owner --no-privileges \
  --single-transaction --exit-on-error <"$DUMP"

say "row counts (old / new)"
mismatch=0
for t in "${TABLES[@]}"; do
  new="$(count "$NEW_CONTAINER" "$NEW_USER" "$NEW_DB" "$t")"
  if running "$OLD_CONTAINER"; then old="$(count "$OLD_CONTAINER" "$OLD_USER" "$OLD_DB" "$t")"; else old="?"; fi
  printf '    %-22s %8s / %s\n' "$t" "$old" "$new"
  if [ "$old" != "?" ] && [ "$old" != "$new" ]; then mismatch=1; fi
done
[ "$mismatch" = 0 ] || say "some counts differ: the old database changed after the dump (the dev server is still running?); FORCE=1 runs it again"

host_port="$(docker port "$NEW_CONTAINER" 5432/tcp | head -n 1 | sed 's/.*://')"
cat <<EOF

Done. Database $NEW_DB in $NEW_CONTAINER (127.0.0.1:$host_port) holds the copy;
$OLD_CONTAINER and its volume are untouched. To switch over:

  1. Stop the dev server (bin/open-mcp-gateway serve) and the fakes if you restart them.
  2. docker stop $OLD_CONTAINER          # keep it (and its volume) as a fallback
  3. docker compose up -d --wait            # the new Postgres on 127.0.0.1:$PORT (recreated if it is on $TEMP_PORT)
  4. In .env: DATABASE_URL=postgres://omp:omp-dev-only@127.0.0.1:$PORT/omp?sslmode=disable
     (keep ENCRYPTION_KEY and API_KEY_PEPPER as they are)
  5. make run                               # applies migration 00013_open_mcp_portal
  6. Create new API keys: keys made before the rename (omcp_) no longer authenticate,
     and OAuth clients sign in again.

Fallback: docker compose down; docker start $OLD_CONTAINER; restore the old DATABASE_URL.
Once you're happy: docker rm $OLD_CONTAINER && docker volume rm open-mcp-gateway_postgres.
EOF
