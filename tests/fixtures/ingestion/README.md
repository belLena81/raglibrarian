# Synthetic ingestion PDF fixtures

This directory contains a deterministic, standard-library-only fixture
generator. The generated PDFs contain short synthetic sentences written for
this project; no uploaded or copyrighted book content is included.

Generate the corpus into a temporary directory:

```sh
go run ./tests/fixtures/ingestion/generate.go -out /tmp/raglibrarian-m4-fixtures
```

The corpus covers a minimal text PDF, structured multipage text, an intentional
blank middle page, an image-only page, a PDF carrying a Standard encryption
dictionary, and a truncated malformed PDF. Generated binaries are intentionally
not committed; black-box tests receive their directory through
`M4_E2E_FIXTURE_DIR`.

Run the dedicated black-box contract after starting an M4 stack:

```sh
M4_E2E_FIXTURE_DIR=/tmp/raglibrarian-m4-fixtures \
M4_E2E_ACCESS_TOKEN='<active librarian or admin access token>' \
M4_E2E_REVOCABLE_ACCESS_TOKEN='<a second disposable active-session token>' \
M4_E2E_PUBLIC_ORIGIN='http://127.0.0.1:5173' \
M4_E2E_EDGE_BASE_URLS='http://127.0.0.1:8080,http://127.0.0.1:8081' \
go -C tests/e2e test -count=1 -v -tags='e2e m4' ./...
```

The tagged suite deliberately fails when an environment value is absent; it
does not silently skip a required M4 contract. Run the connection-cap contract
only against a dedicated stack, adding the `m4_load` tag and setting
`M4_E2E_SSE_CONNECTION_CAP` to that stack's configured cap (at most 10 for the
single-source-IP test) and `M4_E2E_SSE_ACCESS_TOKENS` to the same number of
comma-separated, distinct active-session tokens.

Expected processing outcomes:

| Fixture | Expected outcome |
|---|---|
| `minimal.pdf` | chunks ready |
| `multipage.pdf` | chunks ready with ordered page citations |
| `blank_middle_page.pdf` | chunks ready without a synthetic blank chunk |
| `image_only.pdf` | failed: no extractable text |
| `encrypted.pdf` | failed: encrypted PDF |
| `malformed.pdf` | failed: malformed PDF |
