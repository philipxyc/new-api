#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $EUID == 0 ]] || { echo "Run bootstrap as root" >&2; exit 1; }
[[ $# == 1 && -f "$1/compose.yml" ]] || { echo "Supply the uploaded deployment configuration directory" >&2; exit 1; }
source_dir=$(realpath "$1")
[[ ! -e /opt/new-api ]] || { echo "/opt/new-api already exists; refusing to overwrite" >&2; exit 1; }
docker compose version > /dev/null
install -d -m 700 /opt/new-api /opt/new-api/secrets /opt/new-api/backups
install -m 600 "$source_dir/compose.yml" "$source_dir/Caddyfile" /opt/new-api/
install -m 755 "$source_dir/deploy.sh" /usr/local/sbin/new-api-deploy
install -m 755 "$source_dir/backup.sh" /usr/local/sbin/new-api-backup
python3 - <<'PY'
import pathlib, secrets
root = pathlib.Path("/opt/new-api/secrets")
pg, redis = secrets.token_hex(32), secrets.token_hex(32)
values = {
    "postgres_password": pg + "\n",
    "redis_password": redis + "\n",
    "redis.conf": f"bind 0.0.0.0\nprotected-mode yes\nrequirepass {redis}\nappendonly yes\nmaxmemory 48mb\nmaxmemory-policy noeviction\n",
    "new-api.env": (
        f"SQL_DSN=postgresql://newapi:{pg}@postgres:5432/newapi?sslmode=disable\n"
        f"REDIS_CONN_STRING=redis://:{redis}@redis:6379/0\n"
        f"SESSION_SECRET={secrets.token_hex(32)}\n"
        f"CRYPTO_SECRET={secrets.token_hex(32)}\n"
    ),
}
for name, value in values.items():
    path = root / name
    path.write_text(value)
    path.chmod(0o600)
# Standalone Compose mounts secret files without remapping uid/mode. Redis
# drops root before reading; the enclosing host directory stays root-only.
for name in ("redis_password", "redis.conf"):
    (root / name).chmod(0o444)
PY
printf 'HTTP_BIND=127.0.0.1\n' > /opt/new-api/.env
id newapi-deploy > /dev/null 2>&1 || useradd --create-home --shell /bin/bash newapi-deploy
install -d -m 700 -o newapi-deploy -g newapi-deploy /home/newapi-deploy/.ssh
printf '%s\n' 'newapi-deploy ALL=(root) NOPASSWD: /usr/local/sbin/new-api-deploy *' > /etc/sudoers.d/new-api-deploy
chmod 440 /etc/sudoers.d/new-api-deploy
visudo -cf /etc/sudoers.d/new-api-deploy
echo "Bootstrap complete. Install the forced-command public key before enabling deployment."
