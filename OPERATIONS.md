# Milestone 2 local operations

Milestone 2 keeps Identity data in the dedicated `identity` PostgreSQL database.
`identity_migrator` owns its schema and is used only by the one-shot migration
job; `identity_runtime` receives bounded DML privileges and cannot create or
alter schema objects. Identity starts only after the migration job succeeds.

## Local startup

Create local path configuration and generated credentials without placing
secret values in `.env`:

```bash
cp .env.example .env
make dev-secrets
make bootstrap-verifier
make dev-certs
make compose-config
make stack-up
```

Files under `.dev/secrets` and `.dev/certs` are generated with owner-only
permissions and ignored by Git. Do not print, copy into issue trackers, or
commit their contents. `make dev-secrets` intentionally does not create a
bootstrap verifier. Run `make bootstrap-verifier`; it accepts exactly 32 bytes
from an echo-disabled terminal, refuses overwrite, and persists only the
domain-separated hash.

Mailpit is disposable and connected only to the private Compose backend. Its
inspection UI binds to host loopback at `http://127.0.0.1:8025` by default; set
`MAILPIT_UI_PORT` to change the local port. It must not be used in production.

## Migrations and recovery

```bash
make migrate-identity-up
make migrate-identity-down # local rollback only
```

The migration runner takes a PostgreSQL advisory lock, applies all pending
migrations in one transaction, records SHA-256 checksums, rejects a changed
previously-applied migration, and enforces lock/statement timeouts. A migration
failure rolls back both schema changes and migration-ledger updates. Never edit
an applied migration; add a new version instead.

For a failed local migration, inspect only sanitized container state with
`docker compose ps --all`, correct the migration, and rerun the job. Production
rollback must use an explicitly reviewed forward migration or restore runbook;
the local down target is not a production recovery mechanism.

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
authentication and registration paths are security-sensitive.

Rotate a compromised credential by stopping affected services, replacing only
the owning secret file with mode `0400`, and recreating the affected containers.
Database credential rotation additionally requires changing the PostgreSQL role
password through an approved privileged process. Signing-key rotation must keep
the previous verification key only for the bounded access-token overlap window.
