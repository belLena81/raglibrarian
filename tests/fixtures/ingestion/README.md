# Synthetic ingestion PDF fixtures

This directory contains a deterministic, standard-library-only fixture
generator. The generated PDFs contain short synthetic sentences written for
this project; no uploaded or copyrighted book content is included.

Generate the corpus into a temporary directory:

```sh
go run ./tests/fixtures/ingestion/generate.go -out /tmp/raglibrarian-m4-fixtures
```

The corpus covers a minimal text PDF, structured cross-page text, an intentional
blank middle page, an artifact-confidentiality canary, an image-only page, PDFs
carrying a Standard encryption dictionary with both empty and non-empty user
passwords, a truncated malformed PDF, and a syntactically valid file larger
than 64 MiB that remains safely above the frozen 25 MiB upload bound. Generated
binaries are intentionally not committed; black-box tests receive their
directory through `M4_E2E_FIXTURE_DIR`.

Run the dedicated black-box contract after starting an M4 stack:

```sh
M4_E2E_FIXTURE_DIR=/tmp/raglibrarian-m4-fixtures \
M4_E2E_ACCESS_TOKEN_FILE='/tmp/raglibrarian-m4/access-token' \
M4_E2E_REVOCABLE_ACCESS_TOKEN_FILE='/tmp/raglibrarian-m4/revocable-token' \
M4_E2E_PUBLIC_ORIGIN='http://127.0.0.1:5173' \
M4_E2E_EDGE_BASE_URLS='http://127.0.0.1:8080,http://127.0.0.1:8081' \
go -C tests/e2e test -count=1 -v -tags='e2e m4' ./...
```

For an isolated CI stack, the base M2 lifecycle can hand these sessions to the
separate M4 process without printing them. Point `E2E_M4_ACCESS_TOKEN_OUT` and
`E2E_M4_REVOCABLE_TOKEN_OUT` at absent files inside a mode-0700 temporary
directory; the test creates each file once with mode 0600. The M4 process reads
those same paths through the two `*_TOKEN_FILE` variables above. The temporary
directory must be removed by the surrounding test runner.

Deep artifact and replay checks use private test credentials and never add a
production public route. The M4 Compose test stack provides:

- `M4_E2E_INGESTION_POSTGRES_DSN_FILE`, a read-capable test DSN used to resolve
  the durable manifest receipt and original bounded inbox envelope;
- `M4_E2E_MINIO_ENDPOINT`, `M4_E2E_MINIO_ACCESS_KEY_FILE`,
  `M4_E2E_MINIO_SECRET_KEY_FILE`, and `M4_E2E_MINIO_ARTIFACT_BUCKET`, scoped to
  reading the private artifact bucket;
- `M4_E2E_MINIO_CA_FILE` for a private CA, or `M4_E2E_MINIO_INSECURE=true` only
  for the isolated local Compose network;
- `M4_E2E_RABBITMQ_URI_FILE`, scoped to publishing an idempotent replay to the
  existing Catalog upload exchange and consuming only
  `ingestion.book-uploaded.dlq.v1`. The poison-message contract acknowledges
  only its unique synthetic message and requeues unrelated dead letters.

The worker-recovery contract is intentionally controlled outside the test
process. Set `M4_E2E_RECOVERY_CONTROL_DIR` to a dedicated absolute, non-symlink
directory owned by the test user with mode `0700`, then run only
`TestM4WorkerDownRecovery` while orchestration keeps `ingestion-service`
stopped. The test uploads `minimal.pdf` and atomically creates a regular mode
`0600` marker named `upload-accepted` containing exactly the generated book ID
and a newline. Orchestration validates that marker without logging its content,
restarts the worker, and atomically creates an empty regular mode `0600` marker
named `worker-restarted`. The test then verifies terminal projection plus
singular inbox, job, artifact-set, and deterministic manifest state. Both sides
bound their waits and remove only these two markers. Neither side puts a token,
credential, document body, or diagnostic output in the control directory.

Omitting an optional private inspection credential skips only its dependent
artifact or replay assertion. It does not skip the ordinary upload, processing,
failure taxonomy, SSE, or SLO contracts. Out-of-order projection behavior is
covered deterministically inside Catalog; there is no public event-injection
route.

The tagged suite deliberately fails when a core environment value is absent;
private artifact and replay assertions may skip when their explicitly optional
credentials are absent. Run the connection-cap contract only against a
dedicated stack, adding the `m4_load` tag and setting
`M4_E2E_SSE_CONNECTION_CAP` to that stack's configured cap (at most 10 for the
single-source-IP test) and `M4_E2E_SSE_ACCESS_TOKENS` to the same number of
comma-separated, distinct active-session tokens.

Expected processing outcomes:

| Fixture | Expected outcome |
|---|---|
| `minimal.pdf` | chunks ready |
| `canary.pdf` | chunks ready; canary present only in encrypted artifacts |
| `multipage.pdf` | chunks ready with ordered cross-page citations and carried structure |
| `blank_middle_page.pdf` | chunks ready without a synthetic blank chunk |
| `image_only.pdf` | failed: no extractable text |
| `encrypted.pdf` | failed: encrypted PDF |
| `encrypted_password.pdf` | failed: encrypted PDF |
| `malformed.pdf` | failed: malformed PDF |
| `oversize.pdf` | rejected by the upload boundary |
