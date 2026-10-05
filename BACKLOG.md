# Backlog

## Open

### Update CLAUDE.md and examples from `@v1` to `@v3`
- **Found:** 2026-10-05, while adding a debounce to sync-open-prs
- **Where:** `CLAUDE.md:3`, `.github/workflows/example-caller.yaml:82`
- **Severity:** docs
- **What:** CLAUDE.md's ground rules, release steps and the example callers all say `@v1`, and CLAUDE.md calls this "the customer's local copy", but the live callers use `logiskbrist/...@v3` cross-org. An agent following the release steps would move the wrong tag.
- **Suggested fix:** Replace `v1` with `v3` in CLAUDE.md, README and the example callers, and describe this as the upstream repo.

## Done
