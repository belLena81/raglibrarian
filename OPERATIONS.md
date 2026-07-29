# Local operations

This repository runs the current M2-M7 product stack locally: Edge, Identity,
Catalog, Ingestion, Retrieval, Answer, PostgreSQL, MinIO, RabbitMQ, Qdrant,
Mailpit, TEI or its deterministic test substitute, and provider stubs where a
gate requires them. Milestones 4, 6, and 7 remain release candidates until the
release-candidate completion runbook passes.

## Local startup

Create local path configuration and generated credentials without placing
secret values in `.env`:

```bash
make local-reset  # optional only when deliberately clearing stale local state
cp .env.example .env
make dev-secrets
make test-secrets
make bootstrap-verifier
make dev-certs
make app-bootstrap
```

`make app-bootstrap` delegates to the current local startup path and brings up
the full Compose runtime under the single `raglibrarian` profile. Test-only
containers use the separate `tests` profile, and CI layers
`docker-compose.ci.yml` over `docker-compose.yml`.

Files under `.dev/secrets` and `.dev/certs` are generated with owner-only
permissions and ignored by Git. Do not print, copy into issue trackers, or
commit their contents. `make dev-secrets` intentionally does not create the
bootstrap verifier or test-only e2e credentials. Run `make bootstrap-verifier`
once per local secret set; it refuses to overwrite an existing verifier.

Mailpit is disposable and connected only to the private Compose backend. Its
inspection UI binds to host loopback at `http://127.0.0.1:8025` by default; set
`MAILPIT_UI_PORT` to change the local port. It must not be used in production.

To clear all historical local runtime state for a clean baseline:

```bash
make local-reset
```

This intentionally removes migration-backed local PostgreSQL, RabbitMQ, Qdrant,
secret, certificate, and model artifacts so you redeploy from the current
implementation only. Do not use test reset scripts against runtime or remote
acceptance data.

## Schema bootstrap and recovery

Local Compose applies service-owned schemas through one-shot bootstrap jobs
before long-running services accept traffic. The per-service migration targets
in the Makefile are intentionally disabled for the current local workflow; use
`make app-bootstrap`, `make stack-up`, or the documented release runbooks
instead of invoking service migration targets directly.

For a failed local bootstrap, inspect sanitized container state with
`docker compose --profile raglibrarian ps --all`, correct the failing
configuration or migration, and rerun the startup target. Production recovery
must use an explicitly reviewed forward migration, restore runbook, or
deployment rollback; local reset is not a production recovery mechanism.

## Test-only state

Stateful local, e2e, and CI test targets depend on `make test-secrets` and reset
only allowlisted test PostgreSQL databases and the test Qdrant collection unless
`TEST_RESET_STATE=false` is set for a controlled remote acceptance run. Tests
must never truncate, drop, or rewrite main application databases, Qdrant
collections, object buckets, or uploaded books.

Use `TEST_RESET_STATE=false` only when the target explicitly documents that it
is reusing a prepared fixture stack. Do not use it to make flaky local tests
pass against persistent runtime state.

## M7 lifecycle recovery

Run `scripts/run-local.sh` after updating an existing checkout; it upgrades the
RabbitMQ definitions additively without rotating credentials. Reindex and
delete commands require a stable `Idempotency-Key`; retry a timed-out command
with the same key.

Deletion is complete only after Catalog removes the original object, Ingestion
emits `ingestion.book.artifacts-deleted.v1`, and Retrieval emits
`retrieval.book.index-deleted.v1`. A book remaining in `deleting` indicates a
retryable cleanup dependency. Inspect queue depth and sanitized service health;
do not manually mark the Catalog tombstone complete or delete another service's
rows. After recovery, run `make m7-e2e` with a fresh active librarian token to
prove EPUB evidence, replay-safe reindex, Catalog disappearance, and vector
cleanup.

## Security and release gates

```bash
make ui-check
make ui-audit
make compose-config
make secret-scan
make dockerfile-lint
make image-scan
make full-gates
make integration-gates
```

Scanner images and tool versions are pinned in the Makefile and CI workflow.
An unavailable secret, vulnerability, image, or Dockerfile scan is a failed
gate, not a pass. Do not attach raw application logs to CI artifacts because
authentication, uploads, retrieval, answer prompts, and provider paths are
security-sensitive.

Rotate a compromised credential by stopping affected services, replacing only
the owning secret file with mode `0400`, and recreating the affected containers.
Database credential rotation additionally requires changing the PostgreSQL role
password through an approved privileged process. Signing-key rotation must keep
the previous verification key only for the bounded access-token overlap window.
