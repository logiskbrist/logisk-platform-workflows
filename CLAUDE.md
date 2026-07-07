# Working in this repo — for AI agents

You're editing this customer's copy of `logisk-platform-workflows`. Every app repo in this org calls these via `uses: <this-org>/logisk-platform-workflows/.github/workflows/<name>.yaml@v1`. Same-org references — no cross-org allowlist needed.

Changes here fan out to every app in this org on the next tag. Tolerance for breakage is low. Note also that this is the customer's local copy — the upstream Logiskbrist source lives in `~/Code/Logisk/gb/logisk-platform-workflows/` (or wherever they distribute it). Changes made here don't propagate upstream; upstream changes must be manually pulled in to keep parity with other customers.

## Ground rules

1. **Never break the reusable workflow interface.** Adding a new input with a default is fine. Removing an input, renaming one, or making an optional input required — that's a breaking change and needs a major version bump.
2. **`@v1` is a moving alias.** Callers depend on `@v1` and expect it to stay stable within the major version. If you ship a breaking change, cut `v2` and leave `v1` alone.
3. **Never commit secrets.** No `LOGISK_*` values in this repo. All auth goes through `secrets: inherit` on the caller side.
4. **Preserve `[skip ci]`** in every workflow that pushes a commit back to the caller's repo. Removing it produces the manifest-bump → build → manifest-bump loop that we specifically designed around.
5. **Preserve `::add-mask::`** on any input that could be sensitive. If you `echo` a workflow input without masking first, the value ends up in the caller's log.

## What each workflow does

See [`README.md`](README.md) for the summary table. In one line:

- `build-and-push.yaml` — Docker build → GHCR push. Emits the tag as output.
- `update-{prod,preview}-manifest.yaml` — sed the image tag into kustomize, commit with `[skip ci]`.
- `open-draft-pr.yaml` — mint a GitHub App token, open a draft PR on non-main branches. **Must use App token**, not `GITHUB_TOKEN` (spike finding: `GITHUB_TOKEN`-created PRs don't trigger follow-on workflows).
- `set-secret.yaml` / `delete-secret.yaml` — OIDC → `az keyvault secret {set,delete}`. Enforces `[A-Z_][A-Z0-9_]*` on the name; translates underscore ↔ hyphen for KV compatibility.
- `list-secrets.yaml` — OIDC → `az keyvault secret list` filtered to the calling app. Reverses the naming convention (strips prefix + swaps hyphens back to underscores) so the output is env-var names as the pod sees them. Writes to both `$GITHUB_STEP_SUMMARY` and stdout with `{PROD,PREVIEW}_SECRETS_{START,END}` markers.
- `stale-preview-reaper.yaml` — nightly cron in this repo (not reusable). Closes PRs whose HEAD commit is >7 days old.

**Not in this repo (deliberately):** DB provisioning. The platform provides one Postgres server per customer; apps manage their own databases via Prisma (`prisma migrate deploy` at pod boot, `db.$executeRaw` for programmatic `CREATE DATABASE` if per-preview isolation is needed). See §5.6 of the design doc.
- `example-caller.yaml` — copy-paste template for customer app repos.

## Design assumptions you can rely on

Read the design doc before making non-trivial changes: [`../customer-paas-design.md`](../customer-paas-design.md). Key invariants:

- Every customer's GitHub org has these variables set: `LOGISK_KEYVAULT_NAME`, `LOGISK_AZURE_{CLIENT,TENANT,SUBSCRIPTION}_ID`, `LOGISK_POSTGRES_{SERVER,RG}`, `LOGISK_GH_APP_{ID,INSTALLATION_ID}`. Plus secret `LOGISK_GH_APP_PRIVATE_KEY`.
- The customer's Entra ID app registration has OIDC federated creds for `repo:<org>/*:ref:refs/heads/main` and `repo:<org>/*:pull_request`. It has `Key Vault Secrets Officer` on the KV and `Contributor` on the Postgres server. No AKS access.
- KV secret naming: `<app>-{prod,preview}-<KEY-with-hyphens>`. Rewritten to `<KEY>` in the k8s Secret by ExternalSecrets.
- Image tag scheme: `main-<short-sha>` on main, `pr-<N>-<short-sha>` on PRs.

Do not assume anything else. Ask before adding new required org variables — it's a coordinated rollout.

## Releasing

1. Bug fix or additive change → merge to main.
2. Move the `v1` tag: `git tag -fa v1 -m "…" && git push --tags --force`.
3. If breaking: cut `v2`, do NOT move `v1`. Update `example-caller.yaml` to reference `@v2` and note the migration in `README.md`.

## Testing before release

There's no CI on this repo yet. Do a manual smoke on one of your own customer app repos:

1. Point the caller `uses: …@<your-branch>` instead of `@v1`.
2. Push a commit to that customer app's main. Watch the workflow run.
3. Verify the effect (image pushed, manifest bumped, secret set, DB created — whatever your change was).
4. Only then move the `v1` tag.

Never move `v1` without a smoke test — you'd break every customer at once.

## Known limitations (see design §11)

- `open-draft-pr` needs a real GitHub App installed on every customer org, not just a PAT.
- `stale-preview-reaper`'s `CUSTOMER_ORGS` list is manually maintained. There's no service discovery.
- `provision-preview-db`'s soft cap is per-app, not per-customer. If a customer has 10 apps × 25 previews = 250 DBs, we hit Postgres's connection limit before the cap fires.

None of these are blocking, but be aware when adjusting behavior — the cap check may need to move up a layer, etc.
