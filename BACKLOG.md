# Backlog

## Open

### Update CLAUDE.md and examples from `@v1` to `@v3`
- **Found:** 2026-10-05, while adding a debounce to sync-open-prs
- **Where:** `CLAUDE.md:3`, `.github/workflows/example-caller.yaml:82`
- **Severity:** docs
- **What:** CLAUDE.md's ground rules, release steps and the example callers all say `@v1`, and CLAUDE.md calls this "the customer's local copy", but the live callers use `logiskbrist/...@v3` cross-org. An agent following the release steps would move the wrong tag.
- **Suggested fix:** Replace `v1` with `v3` in CLAUDE.md, README and the example callers, and describe this as the upstream repo.

### Stop verify-prod rolling back over a newer publish
- **Found:** 2026-10-06, while reviewing the sync-open-prs debounce smoke test on godtbrod/deigverkstedet
- **Where:** `.github/workflows/verify-prod.yaml` (rollback step); failing run godtbrod/deigverkstedet actions run 37303449728
- **Severity:** bug (prod)
- **What:** Two publishes 3 min apart (2026-10-05 11:31, 11:34): prod was bumped to `main-32d62dd` and then to `main-9ca02e7` 73 s later, so `main-32d62dd` was never served. verify-prod waited 10 min for it, timed out, and tried to pin prod back to `main-cde0b7c`, two versions older, over the newer good release. Only a rebase conflict on the rollback push stopped it.
- **Suggested fix:** Before waiting or rolling back, re-read the prod manifest. If `newTag` is no longer `expect_tag`, a newer publish owns prod: pass as superseded, never roll back.

## Done
