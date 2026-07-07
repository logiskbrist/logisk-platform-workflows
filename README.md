# logisk-platform-workflows

Reusable GitHub Actions workflows for customer apps deployed on Logiskbrist's AKS. See [`../customer-paas-design.md`](../customer-paas-design.md) for the design.

**Where this repo lives:** in the **customer's own GitHub org**, not `logiskbrist`. Each customer gets a copy seeded during onboarding. Same-org `uses:` references from app repos work without any cross-org access configuration.

## What lives here

| File | Purpose |
|---|---|
| `.github/workflows/build-and-push.yaml` | Build the customer app's Docker image; tag as `main-<sha>` on main pushes and `pr-<N>-<sha>` on PRs; push to GHCR under the calling repo's namespace. Optional `context`, `dockerfile`, `image_suffix` inputs for monorepos with multiple services. |
| `.github/workflows/update-prod-manifest.yaml` | Bump `manifests/prod/kustomization.yaml`'s image tag and push back to main. Adds `[skip ci]` to avoid a bump→build→bump loop. Pass `image_name` to scope the bump to one entry in a multi-image manifest. |
| `.github/workflows/update-preview-manifest.yaml` | Same, but bumps `manifests/preview/kustomization.yaml` on the PR head branch. |
| `.github/workflows/open-draft-pr.yaml` | On any non-main branch push, open a draft PR to main via a **GitHub App token** (not `GITHUB_TOKEN`) so the resulting PR triggers `build`. Idempotent. |
| `.github/workflows/set-secret.yaml` | Write a secret to the customer's Key Vault under the naming convention `<app>-{prod,preview}-<KEY-with-hyphens>`. Uses OIDC to Azure; value is masked in logs. |
| `.github/workflows/delete-secret.yaml` | Mirror of `set-secret` — `az keyvault secret delete`. |
| `.github/workflows/list-secrets.yaml` | Lists env-var names scoped to the calling app. Values never printed. Output goes to both the run's Markdown summary and stdout with `PROD_SECRETS_{START,END}` markers for AI parsing. |
| `.github/workflows/stale-preview-reaper.yaml` | Nightly cron in *this* repo (not called by app repos). Scans customer orgs for `logisk-platform`-tagged repos, closes PRs whose HEAD commit is older than 7 days. Triggers ArgoCD teardown indirectly. |
| `.github/workflows/example-caller.yaml` | Not reusable — copy this file into a customer app repo at `.github/workflows/build.yaml`. |
| `.github/workflows/example-caller-monorepo.yaml` | Not reusable — copy-paste template for a monorepo with multiple services (e.g. `web` + `api`). Uses `image_suffix` / `image_name` and chains bump jobs to serialize pushes. |

## Prerequisites (one-time per customer GH org)

**Create a GitHub App in the customer's own org** with these permissions:
- Repo Contents: Read & Write
- Repo Pull requests: Read & Write
- Repo Metadata: Read
- Org Members: Read

Generate a private key and note the App ID + Installation ID. The App is local to the customer's org — Logiskbrist does not own or install anything on their behalf.

**Set the following as org-level variables** on the customer org:

| Variable | Example value | Used by |
|---|---|---|
| `LOGISK_GH_APP_ID` | `1234567` | `open-draft-pr` |
| `LOGISK_GH_APP_INSTALLATION_ID` | `76543210` | `open-draft-pr` |
| `LOGISK_KEYVAULT_NAME` | `lb-kv-<customer>` | `set-secret`, `delete-secret` |
| `LOGISK_AZURE_CLIENT_ID` | `<client-id-of-customer-app-reg>` | `set-secret`, `delete-secret`, DB workflows |
| `LOGISK_AZURE_TENANT_ID` | `<tenant-id>` | idem |
| `LOGISK_AZURE_SUBSCRIPTION_ID` | `<subscription-id>` | idem |
| `LOGISK_POSTGRES_SERVER` | `pg-<customer>` | (informational; apps read via `/set-secret` PGHOST) |
| `LOGISK_POSTGRES_RG` | `rg-<customer>` | (informational) |

**Set the following as org-level secrets:**

| Secret | Value | Used by |
|---|---|---|
| `LOGISK_GH_APP_PRIVATE_KEY` | The App's PEM private key | `open-draft-pr` |

All reusable workflow inputs default to these names, so a caller usually needs no `with:` overrides for auth.

## How a customer app repo consumes these

Copy [`example-caller.yaml`](.github/workflows/example-caller.yaml) into the app repo at `.github/workflows/build.yaml`. It composes the reusable workflows into the standard flow:

- Push to main → `build-and-push` → `update-prod-manifest` → ArgoCD SCM Provider generator sees the bump → syncs `manifests/prod/`.
- Push to any non-main branch → `open-draft-pr` opens a draft PR.
- PR created → `build-and-push` → `update-preview-manifest` → ArgoCD PullRequest generator sees the PR (and the bumped manifest) → syncs `manifests/preview/`.

**Monorepos with multiple services** (e.g. a `web` frontend and an `api` backend in one repo): copy [`example-caller-monorepo.yaml`](.github/workflows/example-caller-monorepo.yaml) instead. Each service builds under its own `image_suffix` (`ghcr.io/<repo>-web`, `ghcr.io/<repo>-api`) and bumps its own entry in a shared `manifests/{prod,preview}/kustomization.yaml`. To scope secrets per service, pass `app_suffix: -web` (etc.) to `set-secret` / `delete-secret` / `list-secrets`; the KV naming becomes `<repo>-web-{prod,preview}-<KEY>`.

To manage secrets on the app, run one of these from anywhere with `gh` and permission on the repo:

```bash
gh workflow run set-secret.yaml \
  -f name=STRIPE_KEY \
  -f value='sk_live_...'

# Preview override:
gh workflow run set-secret.yaml \
  -f name=STRIPE_KEY \
  -f value='sk_test_...' \
  -f preview=true
```

ExternalSecrets in the cluster pick the new value up on the next refresh (default 1h). Set a shorter `refreshInterval` in the app's `manifests/base/external-secret.yaml` if snappier propagation is worth the extra KV reads.

## Versioning

Pin callers to a tag (`@v1`, `@v1.2.0`), not `@main`. Cut releases with a moving `v1` alias so patch versions don't require caller changes.
