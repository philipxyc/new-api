#!/usr/bin/env bash
set -euo pipefail
umask 077
cd /opt/new-api
set -a
# shellcheck source=/dev/null
source active.env
set +a
install -d -m 700 backups
destination="backups/$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -m 700 "$destination"
trap 'echo "Backup failed; incomplete directory: $destination" >&2' ERR
docker compose -f compose.yml exec -T postgres pg_dump -U newapi -d newapi -Fc > "$destination/database.dump"
test -s "$destination/database.dump"
docker compose -f compose.yml exec -T postgres pg_restore --list < "$destination/database.dump" > /dev/null
docker compose -f compose.yml run --rm --no-deps --entrypoint tar new-api -C /data -czf - . > "$destination/app-data.tar.gz"
cp active.env schema.sha256 compose.yml Caddyfile "$destination/"
cp -a secrets "$destination/"
touch "$destination/COMPLETE"
echo "Backup complete: $destination"
