# logisk-platform-workflows

Reusable GitHub Actions workflows for customer apps deployed on Logiskbrist's AKS. See [`../customer-paas-design.md`](../customer-paas-design.md) for the design.

**Where this repo lives:** in the **customer's own GitHub org**, not `logiskbrist`. Each customer gets a copy seeded during onboarding. Same-org `uses:` references from app repos work without any cross-org access configuration.

## What lives here

| File | Purpose |
|---|---|
| `.github/workflows/build-and-push.yaml` | Build the customer app's Docker image; tag as `main-<sha>` on main pushes and `pr-<N>-<sha>` on PRs; push to GHCR under the calling repo's namespace. Optional `context`, `dockerfile`, `image_suffix` inputs for monorepos with multiple services. |
| `.github/workflows/update-prod-manifest.yaml` | Bump `manifests/prod/kustomization.yaml`'s image tag and push back to main **with the GitHub App token**. Pass `image_name` to scope the bump to one entry in a multi-image manifest. Outputs `previous_tag` (the tag before the bump — feed it to `verify-prod` for rollback) and `image_tag`. |
| `.github/workflows/update-preview-manifest.yaml` | Same, but bumps `manifests/preview/kustomization.yaml` on the PR head branch, also with the App token. Same `previous_tag` / `image_tag` outputs. |
| `.github/workflows/verify-preview.yaml` | Poll the preview environment until it serves the tag in the preview manifest, load the front page, then run the app's `test` script and any Playwright config. Neutral pass while the manifest still says `PLACEHOLDER_TAG`. Outputs `url`, `tag`, `result`. |
| `.github/workflows/verify-prod.yaml` | Same checks against production for a tag you just deployed. On failure, pins the prod manifest back to `previous_tag`, opens a Norwegian issue, and fails the job. Outputs `result` (`ok`/`failed`/`rolled_back`) and `url`. |
| `.github/workflows/ai-review.yaml` | AI code review of the PR diff, posted as one sticky comment. Skipped (passing) without `ANTHROPIC_API_KEY`. Outputs `verdict`, `summary`, `findings_json`. |
| `.github/workflows/lb-review-gate.yaml` | The Logisk Brist review gate for critical apps — holds a sensitive change until the reviewer team approves. No-op on apps that are not marked critical. Outputs `critical`, `sensitive`, `approved`, `reasons`. |
| `.github/workflows/sync-open-prs.yaml` | After each push to the default branch, merge it into every open PR branch (App-token push, so the branch rebuilds and its preview redeploys). Conflicts confined to `manifests/preview/**` are resolved with the branch's own version; any other conflict aborts the merge and upserts one sticky Norwegian comment on the PR — nothing is ever forced. PRs labeled `hold-oppdatering` (input `skip_label`) are left alone entirely. Outputs `synced`, `conflicted`. |
| `.github/workflows/open-draft-pr.yaml` | On any non-main branch push, open a draft PR to main via a **GitHub App token** (not `GITHUB_TOKEN`) so the resulting PR triggers `build`. Idempotent. Outputs `pr_number`. |
| `.github/workflows/set-secret.yaml` | Write a secret to the customer's Key Vault under the naming convention `<app>-{prod,preview}-<KEY-with-hyphens>`. Uses OIDC to Azure; value is masked in logs. |
| `.github/workflows/delete-secret.yaml` | Mirror of `set-secret` — `az keyvault secret delete`. |
| `.github/workflows/list-secrets.yaml` | Lists env-var names scoped to the calling app. Values never printed. Output goes to both the run's Markdown summary and stdout with `PROD_SECRETS_{START,END}` markers for AI parsing. |
| `.github/workflows/example-caller.yaml` | Not reusable — copy this file into a customer app repo at `.github/workflows/build.yaml`. |
| `.github/workflows/example-checks.yaml` | Not reusable — copy into a customer app repo at `.github/workflows/checks.yaml`. The PR checks (`ai-review`, `verify-preview`). |
| `.github/workflows/example-review-gate.yaml` | Not reusable — copy into a customer app repo at `.github/workflows/review-gate.yaml`. The critical-app gate, on `pull_request` + `pull_request_review`. |
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
| `LOGISK_GH_APP_ID` | `1234567` | `open-draft-pr`, `update-{prod,preview}-manifest`, `verify-prod`, `ai-review`, `lb-review-gate` |
| `LOGISK_GH_APP_INSTALLATION_ID` | `76543210` | idem |
| `LOGISK_KEYVAULT_NAME` | `lb-kv-<customer>` | `set-secret`, `delete-secret` |
| `LOGISK_AZURE_CLIENT_ID` | `<client-id-of-customer-app-reg>` | `set-secret`, `delete-secret`, DB workflows |
| `LOGISK_AZURE_TENANT_ID` | `<tenant-id>` | idem |
| `LOGISK_AZURE_SUBSCRIPTION_ID` | `<subscription-id>` | idem |
| `LOGISK_POSTGRES_SERVER` | `pg-<customer>` | (informational; apps read via `/set-secret` PGHOST) |
| `LOGISK_POSTGRES_RG` | `rg-<customer>` | (informational) |

**Set the following as org-level secrets:**

| Secret | Value | Used by |
|---|---|---|
| `LOGISK_GH_APP_PRIVATE_KEY` | The App's PEM private key | `open-draft-pr`, `update-{prod,preview}-manifest`, `verify-prod`, `ai-review`, `lb-review-gate` |
| `ANTHROPIC_API_KEY` | Anthropic API key. **Optional** — without it `ai-review` and the semantic half of `lb-review-gate` are skipped | `ai-review`, `lb-review-gate` |

All reusable workflow inputs default to these names, so a caller usually needs no `with:` overrides for auth.

**Cross-org callers (the normal case since the canonical repo moved to `logiskbrist`): neither `secrets: inherit` nor org `vars` cross the org boundary.** Callers must pass the values explicitly — App/API secrets via a `secrets:` map, Azure/KV identity via `with:` (the `${{ vars.* }}` / `${{ secrets.* }}` expressions evaluate on the caller's side, where the org config is visible):

```yaml
    uses: logiskbrist/logisk-platform-workflows/.github/workflows/update-prod-manifest.yaml@v3
    secrets:
      LOGISK_GH_APP_ID: ${{ secrets.LOGISK_GH_APP_ID }}
      LOGISK_GH_APP_PRIVATE_KEY: ${{ secrets.LOGISK_GH_APP_PRIVATE_KEY }}
```

```yaml
    uses: logiskbrist/logisk-platform-workflows/.github/workflows/set-secret.yaml@v3
    with:
      azure_client_id: ${{ vars.LOGISK_AZURE_CLIENT_ID }}
      azure_tenant_id: ${{ vars.LOGISK_AZURE_TENANT_ID }}
      azure_subscription_id: ${{ vars.LOGISK_AZURE_SUBSCRIPTION_ID }}
      keyvault_name: ${{ vars.LOGISK_KEYVAULT_NAME }}
```

(`ai-review` / `lb-review-gate` additionally take `ANTHROPIC_API_KEY` in the secrets map.) The App ID + installation ID must also exist as org **secrets** (they do on current customer orgs), since org variables never reach the reusable workflows. See the `example-*.yaml` files — they show the full pattern.

## How a customer app repo consumes these

Copy [`example-caller.yaml`](.github/workflows/example-caller.yaml) into the app repo at `.github/workflows/build.yaml`. It composes the reusable workflows into the standard flow:

- Push to main → `build-and-push` → `update-prod-manifest` → ArgoCD SCM Provider generator sees the bump → syncs `manifests/prod/`.
- Push to any non-main branch → `open-draft-pr` opens a draft PR.
- PR created → `build-and-push` → `update-preview-manifest`. The image is built and the preview manifest is bumped for every PR, but a preview Application only appears once the PR carries the `preview` label — the ArgoCD PullRequest generator filters on `github.labels: [preview]`. Add the label with `gh pr edit --add-label preview` to opt in; remove it (or close the PR) to tear the preview down. The nightly `preview-label-reaper` CronJob in each customer's cluster (`argocd` namespace) strips the label from PRs whose `updated_at` hasn't moved in 7 days.

Alongside `build.yaml`, copy [`example-checks.yaml`](.github/workflows/example-checks.yaml) to `.github/workflows/checks.yaml` and [`example-review-gate.yaml`](.github/workflows/example-review-gate.yaml) to `.github/workflows/review-gate.yaml`. They are separate files because they need different triggers — see the next section.

## Review, verify and the critical-app gate

Customer apps are shipped by non-developers through an AI agent, so the safety net is in CI rather than in code review habits. Four reusable workflows provide it, and an org ruleset enforces them as required status checks.

### Check contexts

A check context is `"<caller job id> / <reusable job id>"`. Both halves must match what the ruleset expects, so **the job ids in the reusable workflows are part of the public interface** — renaming one is a breaking change, not a refactor. Give the caller job the same id:

| Check context | Reusable workflow | Runs on |
|---|---|---|
| `ai-review / ai-review` | `ai-review.yaml` | `pull_request` |
| `verify-preview / verify-preview` | `verify-preview.yaml` | `pull_request` |
| `lb-review-gate / lb-review-gate` | `lb-review-gate.yaml` | `pull_request` **and** `pull_request_review` |
| `verify-prod / verify-prod` | `verify-prod.yaml` | `push` to main (after the prod bump) |

`lb-review-gate` needs the `pull_request_review` trigger so the check turns green the moment a reviewer approves — without it, someone would have to push a commit to re-run the gate.

### Manifest pushes now use the App token

`update-prod-manifest`, `update-preview-manifest`, and the `verify-prod` rollback push with the **GitHub App token** instead of `GITHUB_TOKEN`. Two reasons:

1. A "no direct push to main" ruleset lists the App as a bypass actor. `GITHUB_TOKEN` is not, so a `GITHUB_TOKEN` push to main is rejected outright.
2. **Pushes made with `GITHUB_TOKEN` trigger no workflows.** For preview that is fatal: the PR checks would keep evaluating the pre-bump head commit and never see the new preview image. An App-token push fires `pull_request: synchronize`, so `checks.yaml` re-runs against the commit that actually carries the new tag.

Orgs without the App installed keep working — when the App inputs resolve empty, the workflows log a notice and fall back to `GITHUB_TOKEN`. Callers must pass `secrets: inherit` for the App token to be mintable.

This is also why the bump commits carry no `[skip ci]`: the re-run is the point. Callers avoid the bump→build→bump loop with `paths-ignore: manifests/**` instead.

### `ANTHROPIC_API_KEY` is optional

Set it as an org secret to enable AI review. Without it:

- `ai-review` writes "AI-review er ikke aktivert (mangler ANTHROPIC_API_KEY)" to the run summary and passes.
- `lb-review-gate` still runs, but only its path-based classification — the semantic pass is skipped and the PR comment says so.

On a critical app, a semantic check that is *configured but erroring* is treated as sensitive rather than skipped: the gate fails closed and asks for human approval.

### The reviewer team

`lb-review-gate` requests a review from `<org>/<reviewer_team>` (default `logiskbrist-reviewers`) and reads that team's membership to decide whether an approval counts. Both need the App token — `GITHUB_TOKEN` can do neither — so the App's **Organization Members: Read** permission is required. If the team is missing or unreadable the gate fails with a clear error rather than silently letting the change through.

An approval only counts when it was submitted **after** the newest commit that was not authored by the platform bot. A manifest bump landing after a human's approval therefore does not invalidate it.

### What counts as sensitive

Two independent signals, and either one flags the change:

1. **Paths** — auth/access, database, money, platform glue, and secrets/egress globs. `package.json` counts only when the diff touches a dependency line matching `auth|prisma|jsonwebtoken|jose|crypto|passport|stripe|vipps|argon|bcrypt`. Override the whole list with the `sensitive_paths` input (`kategori:glob` per line).
2. **Semantics** — the diff is classified by a model with a strict rubric, biased toward flagging.

The path matcher is deliberately wide: matching is case-insensitive, and a pattern naming a directory also covers everything under it (so `**/payment*` flags `app/(shop)/payment/page.tsx`).

**Monorepos with multiple services** (e.g. a `web` frontend and an `api` backend in one repo): copy [`example-caller-monorepo.yaml`](.github/workflows/example-caller-monorepo.yaml) instead. Each service builds under its own `image_suffix` (`ghcr.io/<repo>-web`, `ghcr.io/<repo>-api`) and bumps its own entry in a shared `manifests/{prod,preview}/kustomization.yaml`. To scope secrets per service, pass `app_suffix: -web` (etc.) to `set-secret` / `delete-secret` / `list-secrets`; the KV naming becomes `<repo>-web-{prod,preview}-<KEY>`.

To manage secrets on the app, run one of these from anywhere with `gh` and permission on the repo:

```bash
gh workflow run set-secret.yaml \
  -f name=STRIPE_KEY \
  -f value='sk_live_...'

# Preview-only value (does NOT propagate to prod; prod value above must also be set):
gh workflow run set-secret.yaml \
  -f name=STRIPE_KEY \
  -f value='sk_test_...' \
  -f preview=true
```

ExternalSecrets in the cluster pick the new value up on the next refresh (default 1h). Set a shorter `refreshInterval` in the app's `manifests/base/external-secret.yaml` if snappier propagation is worth the extra KV reads.

## Versioning

Pin callers to a tag (`@v1`, `@v1.2.0`), not `@main`. Cut releases with a moving `v1` alias so patch versions don't require caller changes.
