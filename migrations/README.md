# Shared SQL migrations

`run-migration.yaml` checks out the calling run's exact commit, submits one SQL
file and follows its result. SQL executes in a short-lived Postgres client Job
on the customer's platform. GitHub receives neither database credentials nor a
cluster token. The database firewall stays unchanged.

The dispatcher is a small Go service using only the standard library. It has
namespace-scoped Job creation and log-read permissions; workers have no cluster
credentials. The deployment bundle for the first customer is in
`logiskbrist/lb-common-iac/ext-prod-setup/migrations/`.

## Calling workflow

```yaml
on:
  workflow_dispatch:
    inputs:
      file:
        required: true
        type: string
permissions:
  contents: read
  id-token: write
jobs:
  migrate:
    uses: logiskbrist/logisk-platform-workflows/.github/workflows/run-migration.yaml@v3
    with:
      file: ${{ inputs.file }}
      endpoint: ${{ vars.LOGISK_MIGRATION_URL }}
```

`database` is an optional assertion, never a routing override. The dispatcher
maps the signed repository ID AND repository name to one database, login and
Kubernetes Secret. New apps need an operator registration and a separate role.
Do not let caller inputs choose namespaces, secrets, images, database hosts or
database roles. Cross-org callers pass the endpoint explicitly; no inherited
secrets or Azure credentials are needed.

## Authorization and execution

The gateway checks the JWT's RS256 signature against GitHub's fixed JWKS URL,
issuer, audience and validity times, then requires:

- An operator-registered repository ID and matching full repository name.
- `event_name=workflow_dispatch` and `environment=production-migrations`.
- The exact registered reusable `job_workflow_ref` **and immutable
  `job_workflow_sha`**. Moving `v3` without updating the registration fails
  closed. Update the registration deliberately when releasing a new version.
- Signed run ID, attempt, commit SHA and actor. These, the SQL path/checksum
  and selected database are recorded on the Job and in dispatcher logs.

Configure required reviewers, prevent self-review, and disable administrator
bypass on the caller's `production-migrations` environment before registering
it. The claim identifies an environment; required reviewer policy is configured
in GitHub, not encoded in a JWT. Administrators who can change environment
protection and the platform registration are trusted operators.

SQL must be a regular, non-symlink UTF-8 file under `script/engangs/`, at most
256 KiB, with no NUL or backslashes. The backslash rule deliberately excludes
psql metacommands, including shell execution and switching connections, and also
rejects backslashes inside comments/literals. Rewrite such SQL or use the
administrator route. This is reviewed SQL execution, not a SQL sandbox: a
migration can change any object its registered database role owns.

Workers use certificate-verified TLS, `psql -X`, `ON_ERROR_STOP`, a 15-second
connection/lock timeout, a 10-minute statement timeout and a 15-minute Job
deadline. A session advisory lock `(731041, 1)` prevents concurrent migrations
within the database, including across repositories. No automatic SQL retries.
The default single transaction rolls back on error. Existing files that contain
their own `BEGIN`/`COMMIT`/`ROLLBACK` remain supported; their explicit transaction
boundaries govern atomicity, so earlier commits cannot be rolled back on a later
error. Operations such as `CREATE INDEX CONCURRENTLY` require the administrator
route. This workflow does not infer whether DDL is additive or take backups.

POST `/migration` accepts `{file, sql, database?}`; GET `/migration` follows the
authenticated run's own Job. Both require a fresh GitHub OIDC token with the
dispatcher's HTTPS origin as audience. Redirects are refused by the client.
Terminal states are `succeeded` or `failed`; pending Jobs remain `running` until
they complete or hit their deadline. Logs are bounded and workflow commands in
SQL output are disabled before printing them in Actions.

## Retry and cancellation semantics

The Job name contains the repository ID and run ID, not run attempt. A lost
response, repeated POST or **rerun of the same GitHub run** observes the original
job. A different SQL checksum/path/commit/database under that run is rejected.
Starting a **new workflow_dispatch** is a new execution; do so only after
inspecting the previous result. There is no checksum-based deduplication across
different GitHub runs, preserving the existing ability to rerun idempotent SQL.

Cancellation of Actions stops following the result; it does not cancel database
execution. The Job remains bounded by its deadline. Never delete an active Job
as a retry mechanism. Jobs are retained as replay records, so there is no TTL
garbage collector. SQL is present in their specs; use schema files rather than
customer-data dumps. Do not prune these records without first adding a durable
replay ledger. This retention is an operational limitation of this first version.

## Verify locally

```sh
cd migrations
go test -race -p 1 ./...
go vet ./...
MIGRATION_POSTGRES_TEST=1 go test -p 1 -run TestPostgresRunner -v ./...
cd ..
actionlint .github/workflows/run-migration.yaml .github/workflows/migration-dispatcher.yaml
docker build -t platform-migration-dispatcher:local migrations
```

The optional integration test starts its own disposable, unpublished Docker
Postgres server and removes it with test cleanup. It verifies rollback,
transaction compatibility, lock exclusion, role provisioning and cross-database
denial. It never loads app secrets. CI enables this test too.

The image workflow tests changes and publishes a commit-tagged image only on
main. Publishing the workflow alias, provisioning the database role and deploying
the service are separate rollout steps; implementing this feature performs none
of those operations automatically.

OIDC claim contract: [GitHub's reference](https://docs.github.com/en/actions/reference/security/oidc).
