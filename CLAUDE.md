# Working in this repo — for AI agents

You're editing this customer's copy of `logisk-platform-workflows`. Every app repo in this org calls these via `uses: <this-org>/logisk-platform-workflows/.github/workflows/<name>.yaml@v1`. Same-org references — no cross-org allowlist needed.

Changes here fan out to every app in this org on the next tag. Tolerance for breakage is low. Note also that this is the customer's local copy — the upstream Logiskbrist source lives in `~/Code/Logisk/gb/logisk-platform-workflows/` (or wherever they distribute it). Changes made here don't propagate upstream; upstream changes must be manually pulled in to keep parity with other customers.

## Ground rules

1. **Never break the reusable workflow interface.** Adding a new input with a default is fine. Removing an input, renaming one, or making an optional input required — that's a breaking change and needs a major version bump.
2. **`@v1` is a moving alias.** Callers depend on `@v1` and expect it to stay stable within the major version. If you ship a breaking change, cut `v2` and leave `v1` alone.
3. **Never commit secrets.** No `LOGISK_*` values in this repo. All auth goes through `secrets: inherit` on the caller side.
4. **Do NOT add `[skip ci]`** to bump commits. It was deliberately dropped: the preview bump *must* re-trigger the PR checks so they evaluate the commit that carries the new image. Callers break the bump → build → bump loop with `paths-ignore: manifests/**` instead.
5. **Manifest bumps and rollbacks push with the App token.** `update-{prod,preview}-manifest` and the `verify-prod` rollback must mint the GitHub App token and check out with it. `GITHUB_TOKEN` cannot bypass the no-direct-push-to-main ruleset, and a `GITHUB_TOKEN` push triggers no workflows at all — which would leave PR checks stuck on the pre-bump commit. Always keep the fall back to `GITHUB_TOKEN` when the App inputs resolve empty, so orgs without the App keep working.
6. **Job ids in `ai-review`, `verify-preview`, `verify-prod`, and `lb-review-gate` are public interface.** The org ruleset requires the check contexts `<job id> / <job id>`. Renaming a job there is a breaking change.
7. **Preserve `::add-mask::`** on any input that could be sensitive. If you `echo` a workflow input without masking first, the value ends up in the caller's log.
8. **`secrets: inherit` does NOT cross the org boundary.** GitHub only honors `inherit` when caller and reusable workflow live in the same org or enterprise; customer app repos call this repo cross-org, so inherited org secrets arrive *empty* — `create-github-app-token` then silently takes the `GITHUB_TOKEN` fallback and bump pushes stop re-triggering checks. Any workflow that needs a secret cross-org must declare it under `on.workflow_call.secrets` and callers must map it explicitly (`secrets: {LOGISK_GH_APP_PRIVATE_KEY: ${{ secrets.LOGISK_GH_APP_PRIVATE_KEY }}}`). `sync-open-prs.yaml` does this; the older workflows still rely on inherit and are affected (verified empirically on godtbrod 2026-09-01, and documented in GitHub's reusing-workflows docs).

## What each workflow does

See [`README.md`](README.md) for the summary table. In one line:

- `build-and-push.yaml` — Docker build → GHCR push. Emits the tag as output, and passes it as the `IMAGE_TAG` build arg so the app can report its own version from `/api/health` (the verify workflows use that to tell the new image from the old one).
- `update-{prod,preview}-manifest.yaml` — write the image tag into kustomize and push, **using the App token**. Outputs `previous_tag` (the value before the bump) and `image_tag`. No `[skip ci]` — see ground rule 4.
- `open-draft-pr.yaml` — mint a GitHub App token, open a draft PR on non-main branches. **Must use App token**, not `GITHUB_TOKEN` (spike finding: `GITHUB_TOKEN`-created PRs don't trigger follow-on workflows). Outputs `pr_number`.
- `sync-open-prs.yaml` — called from the caller's main-push path: merges the freshly pushed default branch into every open PR branch so testversjoner never go stale. **Pushes with the App token** so each synced branch rebuilds and its preview redeploys. Conflicts confined to `manifests/preview/**` (a squash-merged PR drags its preview bump into main) are resolved with the branch's own version; any other conflict is aborted, never forced, and the PR gets one sticky Norwegian comment («assistenten oppdaterer testversjonen neste gang du jobber videre på den») that is deleted again once a later sync succeeds. Serialized per repo via a concurrency group. Conflicts don't fail the job; only unexpected push failures do. Escape hatch: a PR labeled `hold-oppdatering` (input `skip_label`) is left alone entirely — the agent sets/removes the label for a long-running testversjon or one mid-demo. Outputs `synced`, `conflicted`.
- `verify-preview.yaml` — reads the preview manifest's `newTag`, polls `<preview>/api/health` until it reports that tag (or, when the app exposes no `tag` field, until it has been up for an extra 60s and warns), checks the front page, then runs the app's `test` script and any Playwright config under `verify/`. A manifest still on `PLACEHOLDER_TAG` is a neutral pass — nothing has been built yet. Norwegian step summary, English detail in the log.
- `verify-prod.yaml` — the same shape against production for a tag that was just deployed, with a 90s settle when the app reports no tag. On failure it pins the prod manifest back to `previous_tag` (App token, same retry/rebase as the bumpers), opens a Norwegian issue explaining the rollback, and fails the job. Rollback needs `previous_tag` to be present and different from `expect_tag`.
- `ai-review.yaml` — fetches the PR diff, drops lockfiles/manifests/images, sends it to the Anthropic Messages API with a strict-JSON rubric, and upserts one sticky PR comment. The comment's marker carries the diff's sha256, so a re-run triggered by the manifest bump reuses the previous verdict instead of paying for a second review. Fails only on findings that are high severity *and* high confidence. No `ANTHROPIC_API_KEY` → skip and pass.
- `lb-review-gate.yaml` — no-op unless the repo carries the `logisk-critical` topic or the `criticality=critical` custom property. On critical apps it classifies the change by path and (if not already flagged) by semantics, then labels the PR, requests review from the reviewer team, and holds the check red until a team member approves after the last non-bot commit. Fails closed: an erroring semantic check counts as sensitive, and a missing reviewer team is an error rather than a pass.
- `set-secret.yaml` / `delete-secret.yaml` — OIDC → `az keyvault secret {set,delete}`. Enforces `[A-Z_][A-Z0-9_]*` on the name; translates underscore ↔ hyphen for KV compatibility.
- `list-secrets.yaml` — OIDC → `az keyvault secret list` filtered to the calling app. Reverses the naming convention (strips prefix + swaps hyphens back to underscores) so the output is env-var names as the pod sees them. Writes to both `$GITHUB_STEP_SUMMARY` and stdout with `{PROD,PREVIEW}_SECRETS_{START,END}` markers.
- `stale-preview-reaper.yaml` — nightly cron in this repo (not reusable). Closes PRs whose HEAD commit is >7 days old.

**Not in this repo (deliberately):** DB provisioning. The platform provides one Postgres server per customer; apps manage their own databases via Prisma (`prisma migrate deploy` at pod boot, `db.$executeRaw` for programmatic `CREATE DATABASE` if per-preview isolation is needed). See §5.6 of the design doc.
- `example-caller.yaml` / `example-checks.yaml` / `example-review-gate.yaml` — copy-paste templates for customer app repos. Three files, because build/publish runs on `push` + `pull_request`, the PR checks on `pull_request`, and the gate additionally on `pull_request_review`.

## Design assumptions you can rely on

Read the design doc before making non-trivial changes: [`../customer-paas-design.md`](../customer-paas-design.md). Key invariants:

- Every customer's GitHub org has these variables set: `LOGISK_KEYVAULT_NAME`, `LOGISK_AZURE_{CLIENT,TENANT,SUBSCRIPTION}_ID`, `LOGISK_POSTGRES_{SERVER,RG}`, `LOGISK_GH_APP_{ID,INSTALLATION_ID}`. Plus secret `LOGISK_GH_APP_PRIVATE_KEY`, and optionally `ANTHROPIC_API_KEY` (absent → AI review and the semantic gate check are skipped, never failed).
- The GitHub App has Contents RW, Pull requests RW, Metadata R, and **Org Members R**. The last one is what lets `lb-review-gate` read the reviewer team; without it the gate errors rather than passing.
- Customer-facing text is Norwegian bokmål, plain language. Avoid developer vocabulary in the lines a customer reads — say «publisering», «testversjon», «godkjenning fra Logisk Brist», not PR/merge/branch. English detail belongs in the run log or a collapsed block for the reviewer.
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
