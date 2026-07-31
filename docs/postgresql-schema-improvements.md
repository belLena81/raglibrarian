# PostgreSQL schema improvement recommendations

This note records database normalization and search-optimization recommendations
for the current RAGLibrarian architecture. It is design guidance, not an
approved migration plan.

## Current schema shape

RAGLibrarian uses service-owned PostgreSQL schemas:

- `identity` owns users, sessions, refresh tokens, verification/reset state, and
  email outbox records.
- `catalog` owns the book aggregate, catalog outbox/inbox tables, and lifecycle
  commands.
- `ingestion` owns processing jobs, artifact-set state, event inbox/outbox
  records, deletion handling, and content-selection workflow state.
- `retrieval` owns metadata facts, manifest facts, indexing jobs, evidence
  projections, lifecycle state, lexical search state, and summary-assessment
  cache rows.

There are intentionally no foreign keys across service schemas. Services
coordinate through event inbox/outbox tables, shared event IDs, book IDs,
command IDs, lifecycle versions, and broker delivery. Keep that boundary: a
cross-service foreign key would make the schema look more normalized while
weakening service ownership and independent recovery.

## Normalization assessment

The transactional ownership tables are mostly normalized enough for the current
service boundaries. The largest denormalized tables are intentional read models:

- `retrieval.evidence` repeats book metadata such as title, author, tags,
  publication year, and media type. This violates strict third-normal-form
  expectations if treated as source-of-truth data, but it is appropriate as a
  search projection because retrieval must return evidence without joining
  metadata for every candidate.
- `catalog.books` combines metadata, processing state, object-storage facts,
  lifecycle state, and deletion flags. This is acceptable while it remains the
  Catalog aggregate persistence table and common list/get queries benefit from
  one row.
- `identity.users` stores role-specific account rows. Further normalization into
  accounts, profiles, roles, and review records should only happen if the domain
  model changes to one identity with multiple roles.

Recommended principle:

```text
Transactional source state: normalize around the owning service aggregate.
Search/read projections: denormalize deliberately and rebuild from facts.
Event inbox/outbox tables: optimize for idempotency, leasing, and replay.
Vector search: keep Qdrant as the vector store unless the project chooses pgvector.
Lexical search: optimize PostgreSQL full-text search over retrieval projections.
```

## Highest-value search improvements

### 1. Add a stored weighted search vector for evidence

The current lexical index is expression-based over title, author, chapter,
section, and passage. A stored generated `tsvector` makes the query simpler,
avoids recomputing the vector during ranking, and allows weighted fields.

Candidate shape:

```sql
ALTER TABLE retrieval.evidence
ADD COLUMN search_vector tsvector
GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('simple', coalesce(author, '')), 'B') ||
    setweight(to_tsvector('simple', coalesce(chapter, '')), 'B') ||
    setweight(to_tsvector('simple', coalesce(section, '')), 'C') ||
    setweight(to_tsvector('simple', coalesce(passage, '')), 'D')
) STORED;

CREATE INDEX retrieval_evidence_search_vector_idx
ON retrieval.evidence
USING GIN (search_vector);
```

Then lexical search should use `search_vector @@ websearch_to_tsquery(...)` and
`ts_rank_cd(search_vector, ...)`.

Expected benefit: better exact/lexical recall latency and better ranking of
title/chapter/section hits compared with passage-only matches.

### 2. Filter by active lifecycle state inside lexical SQL

Retrieval currently applies visibility filtering after candidate retrieval. That
is safe, but large indexes can waste work ranking stale jobs.

Lexical search should filter active/indexed jobs before ranking:

```sql
FROM retrieval.evidence AS e
JOIN retrieval.index_jobs AS j ON j.id = e.job_id
JOIN retrieval.book_lifecycle AS l ON l.book_id = e.book_id
WHERE j.state = 'indexed'
  AND l.state = 'active'
  AND l.active_job_id = e.job_id
  AND e.search_vector @@ lexical_query.tsquery
```

Supporting indexes:

```sql
CREATE INDEX retrieval_index_jobs_active_idx
ON retrieval.index_jobs (id, book_id)
WHERE state = 'indexed';

CREATE INDEX retrieval_book_lifecycle_active_job_idx
ON retrieval.book_lifecycle (active_job_id)
WHERE state = 'active';
```

Expected benefit: fewer stale candidates, lower rank/sort cost, and stronger
database-level correctness.

### 3. Add filter indexes for common retrieval constraints

If search filters by book, job, author, tags, media type, or lifecycle state,
add indexes that match those predicates:

```sql
CREATE INDEX retrieval_evidence_job_idx
ON retrieval.evidence (job_id);

CREATE INDEX retrieval_evidence_book_job_idx
ON retrieval.evidence (book_id, job_id);

CREATE INDEX retrieval_evidence_author_lower_idx
ON retrieval.evidence (lower(author));

CREATE INDEX retrieval_evidence_media_type_idx
ON retrieval.evidence (media_type);

CREATE INDEX retrieval_evidence_tags_gin_idx
ON retrieval.evidence
USING GIN (tags);
```

Only add these after confirming the corresponding predicates are used by the
production query path. Every index has write and maintenance cost.

### 4. Keep dense and sparse search hybrid

Do not move all search into PostgreSQL unless the project explicitly chooses a
different retrieval architecture. Current project goals are better served by:

- Qdrant for vector chunk/document recall.
- PostgreSQL `tsvector`/GIN for exact sparse lexical recall.
- application-level fusion and relevance assessment.

This preserves exact protocol/code/title matches while retaining semantic
retrieval for natural-language questions.

## Catalog improvements

Catalog list queries filter out deleted books and order by newest book:

```sql
WHERE processing_status <> 'deleted'
ORDER BY created_at DESC, id DESC
```

Add a matching partial index:

```sql
CREATE INDEX catalog_books_active_created_at_id_idx
ON catalog.books (created_at DESC, id DESC)
WHERE processing_status <> 'deleted';
```

If UI search/filtering over catalog metadata becomes important, consider:

```sql
CREATE INDEX catalog_books_status_idx
ON catalog.books (processing_status);

CREATE INDEX catalog_books_tags_gin_idx
ON catalog.books
USING GIN (tags);

CREATE INDEX catalog_books_author_lower_idx
ON catalog.books (lower(author));
```

For partial-title or typo-tolerant metadata search, consider `pg_trgm` on
`title` and `author`. Avoid trigram indexing full passage text until measured;
it can be expensive.

## Identity improvements

Identity is already normalized around users, sessions, refresh tokens, and
challenge/outbox state. Useful operational indexes:

```sql
CREATE INDEX identity_sessions_expiry_idx
ON identity.sessions (expires_at);

CREATE INDEX identity_refresh_tokens_expiry_idx
ON identity.refresh_tokens (expires_at);
```

These help cleanup and operational diagnostics as session volume grows.

Do not split `identity.users` just for normalization. A split into account,
profile, role, and approval tables is only justified if the domain model changes
from role-specific user records to one account with multiple roles.

## Ingestion improvements

Ingestion already separates jobs, retry dispatches, artifact sets, lifecycle
fences, deletion inbox records, and content-selection state. Useful candidate
indexes:

```sql
CREATE INDEX ingestion_jobs_state_created_idx
ON ingestion.jobs (state, created_at, id);

CREATE INDEX ingestion_jobs_book_lifecycle_idx
ON ingestion.jobs (book_id, lifecycle_version);
```

Add them only if worker pickup, lifecycle lookup, or diagnostics show sequential
scans under realistic data volumes.

## Optional future split for retrieval search projections

If `retrieval.evidence` becomes both write-heavy and search-heavy, split the
search projection from the evidence payload:

```text
retrieval.evidence
  evidence_id
  chunk_id
  job_id
  book_id
  passage
  page_start
  page_end
  content_sha256
  created_at

retrieval.evidence_search
  evidence_id
  job_id
  book_id
  title
  author
  publication_year
  tags
  chapter
  section
  media_type
  search_vector
```

This is less normalized than joining back to metadata facts on every query, but
it makes the read model rebuildable and lets search-specific indexing evolve
without bloating the core evidence payload table.

Do not make this split preemptively. First confirm evidence-table size,
write-rate, vacuum pressure, and query plans.

## What not to change

- Do not add cross-service foreign keys.
- Do not normalize `retrieval.evidence` out of usefulness on the search hot
  path.
- Do not hardcode ranking, page sizes, timeout, retry, or cache policy in SQL or
  application logic. Thread policy through config.
- Do not let generated proto, HTTP, provider, Qdrant, or SQL types leak into
  domain/application code.
- Do not mutate main application databases from unit or E2E tests; schema
  performance checks need isolated migrated databases.

## Suggested implementation order

1. Capture `EXPLAIN (ANALYZE, BUFFERS)` for current catalog list and retrieval
   lexical search on realistic data.
2. Add stored weighted `retrieval.evidence.search_vector`.
3. Change lexical search to use `search_vector`.
4. Push active lifecycle/indexed-job filtering into lexical SQL.
5. Add the catalog active-list partial index.
6. Add cleanup/job/filter indexes only when query plans justify them.
7. Re-run integration tests and compare query plans before and after.
