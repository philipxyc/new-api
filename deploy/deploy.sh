#!/usr/bin/env bash
set -euo pipefail
umask 077
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
cd /opt/new-api
exec 9>/run/lock/new-api-deploy.lock
flock -w 900 9

# SSH only admits one digest, never a path, shell program or Compose override.
if [[ $# != 1 || ! "$1" =~ ^deploy\ (sha256:[a-f0-9]{64})$ ]]; then
  echo "Expected: deploy sha256:<64 lowercase hex characters>" >&2
  exit 2
fi
image="ghcr.io/philipxyc/new-api@${BASH_REMATCH[1]}"
registry_config=$(mktemp -d /run/new-api-registry.XXXXXX)
trap 'rm -rf -- "$registry_config"' EXIT
IFS= read -r registry_token
[[ -n "$registry_token" ]] || { echo "A short-lived, read-only registry token is required" >&2; exit 2; }
printf '%s' "$registry_token" | docker --config "$registry_config" login ghcr.io -u philipxyc --password-stdin > /dev/null
unset registry_token
docker --config "$registry_config" pull --quiet "$image"

schema=$(docker image inspect --format '{{ index .Config.Labels "org.newapi.schema" }}' "$image")
[[ "$schema" =~ ^[a-f0-9]{64}$ ]] || { echo "Image has no valid schema fingerprint" >&2; exit 1; }
if [[ -f active.env ]]; then
  previous=$(sed -n 's/^NEW_API_IMAGE=//p' active.env)
  [[ "$previous" =~ ^ghcr.io/philipxyc/new-api@sha256:[a-f0-9]{64}$ ]] || exit 1
  if [[ "$(cat schema.sha256)" != "$schema" ]]; then
    [[ -f approved-migration && "$(cat approved-migration)" == "$image" ]] || {
      echo "Database-affecting source changed. Back up, rehearse migration/restore, then approve this exact image." >&2
      exit 1
    }
  fi
  if [[ "$previous" != "$image" ]]; then
    /usr/local/sbin/new-api-backup
    cp active.env previous.env
    cp schema.sha256 previous-schema.sha256
  fi
fi

export NEW_API_IMAGE="$image"
docker compose -f compose.yml config --quiet
printf 'NEW_API_IMAGE=%s\n' "$image" > active.env.tmp
mv active.env.tmp active.env
printf '%s\n' "$schema" > schema.sha256.tmp
mv schema.sha256.tmp schema.sha256
docker compose -f compose.yml up -d --wait --wait-timeout 240

# Do not automatically roll back an image after it may have migrated the DB.
curl --fail --silent --max-time 10 http://127.0.0.1/api/status |
  python3 -c 'import json,sys; assert json.load(sys.stdin)["success"] is True'
[[ "$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 10 http://127.0.0.1/v1/models)" == 401 ]] || {
  echo "Unauthenticated model requests must be rejected" >&2
  exit 1
}
if [[ -f secrets/smoke.curl ]]; then
  curl --fail --silent --max-time 120 --config secrets/smoke.curl |
    python3 -c 'import json,sys; r=json.load(sys.stdin); assert r.get("choices") and r["choices"][0].get("message")'
else
  if [[ -f allow-pending-authorization && ! -f release.env ]]; then
    printf 'pending_copilot_authorization\n' > verification.status
    echo "::warning::Site deployed and health/unauthorized-access checks passed. Copilot authorization and authenticated inference are explicitly deferred."
    exit 0
  fi
  echo "Authenticated inference check not configured; deployment cannot be marked verified." >&2
  # Initial setup must finish before the inference probe can be provisioned.
  exit 3
fi
printf 'NEW_API_IMAGE=%s\n' "$image" > release.env
printf 'authenticated_inference_passed\n' > verification.status
rm -f approved-migration allow-pending-authorization
echo "Deployment and authenticated inference verified: $image"
