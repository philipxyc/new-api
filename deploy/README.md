# New API production deployment

This deployment builds this application fork, including the native GitHub
Copilot provider, without a separate proxy container or deployment repository.
Keep upstream New API and QuantumNous attribution, licenses, and source offers.
The application is AGPL-3.0: users interacting with a modified service over a
network must have access to its corresponding source. This public fork and
commit-addressed images provide the source; retain the UI's source links.
Review AGPL section 13 and obtain legal advice for distribution-specific questions.

## Initial installation

The initial operator-requested endpoint is **HTTP**, not HTTPS. Public HTTP does
not protect passwords, cookies, or API keys. Do not treat this configuration as
an authenticated production security baseline. Perform bootstrap and automated
credential-bearing probes on the server over verified SSH. HTTPS and secure
cookies remain required before exposing sensitive client use on an untrusted
network.

1. Verify the server SSH host key through a separately authenticated channel.
2. Install Docker Engine and Compose, inspect existing workloads, routes and
   firewalls. The frontend subnet `172.30.80.0/28` must not overlap a host route.
3. Upload this directory to an administrator-owned staging directory. Run
   `sudo bash /path/to/staging/bootstrap.sh /path/to/staging` once.
   Configuration and secrets live in root-owned `/opt/new-api`, never Git.
4. Install a dedicated public key for `newapi-deploy` in its `authorized_keys`:

   ```text
   command="sudo -n /usr/local/sbin/new-api-deploy \"$SSH_ORIGINAL_COMMAND\"",restrict ssh-ed25519 PUBLIC_KEY
   ```

   The account is not in the Docker group and cannot upload scripts, forward
   ports, allocate a terminal, or select a Compose file. Root owns the fixed
   deployment script. Use a different administrator authentication path for
   maintenance, and remove temporary unrestricted bootstrap key entries.
5. Configure the `production` GitHub environment to allow only `main`. Set its
   `PRODUCTION_SSH_KEY` secret and the repository variable
   `PRODUCTION_KNOWN_HOSTS` to the verified host key line. Never disable host
   verification or run PR code on the server.
6. Enable Actions on the fork. A push to `main` checks the provider and config,
   builds once, publishes to `ghcr.io/philipxyc/new-api`, then deploys exactly
   that build's immutable digest. The deployment job sends only its short-lived,
   read-only Packages token through SSH; Docker credentials are held under
   `/run` and removed afterwards. The write-capable build token never reaches
   the server.
7. The first deployment brings the site up but deliberately reports
   **unverified** (exit 3) until setup, Device Flow, and the authenticated smoke
   request are configured. Its digest is retained in `active.env`; `release.env`
   records only a release that passed authenticated inference. An operator who
   explicitly defers Device Flow can create the root-owned file
   `/opt/new-api/allow-pending-authorization`. Until the first fully verified
   release, this permits deployment with health and unauthorized-access checks,
   an explicit Actions warning, and `verification.status` set to
   `pending_copilot_authorization`. This never claims authenticated inference
   passed, and the allowance is removed after the first successful probe.
   Ingress initially
   binds loopback to prevent an anonymous first-setup takeover. Complete setup
   over administrator SSH, then explicitly set `HTTP_BIND=0.0.0.0` in
   `/opt/new-api/.env` and recreate the proxy to publish port 80.

Only the HTTP reverse proxy is public. New API's maintenance port is loopback
only; PostgreSQL and Redis have no published ports and an internal network.
Copilot credentials are stored by the existing provider-credential lifecycle
in PostgreSQL and persist when containers are replaced. Redis is not their
source of truth. Named volumes must not be deleted with `down -v`.
Set a public site announcement linking to this fork's corresponding source,
while retaining the upstream New API and QuantumNous links and attribution.

## Native Copilot channel

Create a channel using the bundled **GitHub Copilot** provider, leave its key
empty, save, then use the existing channel sign-in dialog. Complete the Device
Flow on `https://github.com/login/device` yourself. This authorizes upstream
inference; it is unrelated to GitHub website sign-in.

The GitHub Copilot app's public client ID is not a secret. Its internal token
exchange and inference API are **unsupported**, not a public GitHub API contract.
Respect GitHub terms, subscription entitlements, organization policies, rate
limits and billing. Do not pool, redistribute or resell a subscription.

Use the official API origin assigned to the subscription as the channel Base
URL (`https://api.githubcopilot.com`, `https://api.business.githubcopilot.com`,
or `https://api.enterprise.githubcopilot.com`). Credentials never go to an
arbitrary Base URL or a redirected credential exchange. Expiring inference
tokens are renewed using the server-side GitHub authorization.

Fetch the accessible models after sign-in. Discovery excludes embeddings and
decision models. Verified Responses-only model IDs use native `/responses`;
Chat Completions and Messages clients use the host's existing Responses
converters for those IDs. Other chat models retain their Chat Completions
routes. No extra proxy, authentication state machine or billing engine is used.
The endpoint bindings are an explicit catalog snapshot, not family-name guesses.
Streaming, tools and usage settlement remain the existing relay's responsibility.

Use client aliases such as `github-copilot/gpt-6.1-terra` with channel
`model_mapping` set to `{"github-copilot/gpt-6.1-terra":"gpt-6.1-terra"}`.
The prefix isolates GitHub pricing from identically named direct-provider
models. Alias-to-upstream mappings must be configured before enabling aliases.
Built-in expressions record the official pricing source and verification date
in `setting/billing_setting/builtin_billing.go`; input, output, cache reads,
cache writes and applicable whole-context thresholds are priced there.
Administrator overrides and group multipliers still apply. Use a multiplier
of 1 for published-rate deductions. These are estimated usage charges, not
reconciliation of GitHub subscription allowances or invoices.

Unpublished rates (including MAI-Code-2 and Sol fast) and time-limited Gemini
promotions are deliberately not inferred from other providers' prices.
Keep those models disabled until an explicit, reviewed price is configured.
Discovery is not automatic enablement, nor proof that inference is permitted.
In particular, GitHub can issue a credential and list models while rejecting
inference with `billing_not_configured`. If the account has several Copilot
licenses, choose the intended **Usage billed to** entity at
`https://github.com/settings/copilot/features`; do not repeat Device Flow.
Entity names can themselves contain words such as “feature flag” or “Open Source.”
New API quotas do not guarantee GitHub subscription capacity.

### Same-day model updates

Refresh the authenticated account catalog, not just a public model list. Check
the new ID's endpoint capabilities and GitHub's published rates. Existing chat
protocols need only a reviewed alias, mapping and price; new Responses-only IDs
also need an explicit plugin route binding. Changed protocols require a plugin
change, not a guessed route. Keep unpriced or unverified models disabled.
Run a bounded inference, stream/tool probe and usage/quota reconciliation before
enabling a model for clients. Ship source changes through the normal checks,
immutable image build and digest deployment. Preserve explicit admin overrides
and recheck promotional expiry dates. LiteLLM is a useful compatibility
reference, not the authority for Copilot prices or a same-day rollout guarantee.

Provision a root-only `/opt/new-api/secrets/smoke.curl` after creating a limited
New API client token and choosing an accessible model. Use curl's config format:
`url = "http://127.0.0.1:3000/v1/chat/completions"`, `request = "POST"`,
an authorization header, `Content-Type: application/json`, and a JSON `data`
value containing the selected model and a minimal user message. Never put the
real token in a shell argument, Git, an image, or workflow output.
The deploy script requires a successful authenticated inference response.
For probes through Docker's host-published loopback port, the application's
observed source IP may be the private Docker gateway, not `127.0.0.1`.
Restrict the smoke token to the verified source address plus its specific
model and finite quota; do not remove its IP restriction to make a probe pass.
Run the workflow again after provisioning; previously built digests may also be
redeployed manually without rebuilding.

## Backups, migrations and rollback

`sudo /usr/local/sbin/new-api-backup` creates a root-only consistent PostgreSQL
custom-format dump, application data archive, configuration and secrets under
`/opt/new-api/backups`. Only directories with `COMPLETE` are successful.
It validates dump readability, **not a restore**. These backups contain usable
credentials. Keep them on encrypted storage, encrypt before off-host transfer,
restrict access, and establish monitored off-host retention before production.
No automatic destructive retention is installed.

The deployment script serializes with `flock`; Actions also serializes production
deployments. An update backs up first and retains `previous.env` and
`previous-schema.sha256`. Model/database and ORM dependency fingerprints are
checked separately from the image. A change requires rehearsal on a restored
database, explicit approval of the exact immutable image in
`/opt/new-api/approved-migration`, and a maintenance window. Treat changes to
other migration paths the same way even if the conservative fingerprint does not
detect them. New API runs its existing migrations at startup; this is not a
transactional, separately reversible migration command.

To restore, stop ingress and New API, retain a snapshot of the current data,
start an isolated PostgreSQL of the pinned version, restore with
`pg_restore --exit-on-error --no-owner --no-acl -U newapi -d newapi`, restore
the matching application data and secrets, and verify data plus authenticated
inference before switching traffic. Never test a restore over the only live
database. Rehearse this procedure on a separate Compose project and verify
schema, channel credentials, users, quotas and application objects.

For an image-only rollback after confirming no incompatible migration ran:
copy `previous.env` to `active.env` and `previous-schema.sha256` to `schema.sha256`
after preserving the current manifests. From `/opt/new-api`, export the previous
image with `set -a; source active.env; set +a`, then run
`docker compose -f compose.yml up -d --wait`.
Re-run health and authenticated inference checks before marking it successful.
Do not automatically roll back a failed image after it might have migrated the
database; an image rollback cannot undo a database migration.

## Security verification references

Apply OWASP ASVS 5.0.0 and the Authentication, Session Management and OAuth2
Cheat Sheets. The existing provider login flow requires an authenticated
administrator session and provides
session binding, single-use completion, expiry, server-side device state and
poll throttling. Provider regression tests cover device pending/slow-down,
authorization failure, expiry, token renewal, entitlement failure, model
filtering and credential destination validation without real credentials.
This HTTP bootstrap does **not** satisfy the TLS/session transport controls;
do not claim ASVS compliance. Live Device Flow, inference, persistence and
backup-restore checks are separate operator acceptance requirements.
