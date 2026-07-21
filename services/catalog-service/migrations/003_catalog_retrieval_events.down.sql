DELETE FROM catalog.processing_inbox
WHERE event_type IN ('retrieval.book.indexed.v1', 'retrieval.book.indexing-failed.v1');

UPDATE catalog.books
SET processing_status = 'processing',
    processing_stage = 'chunks_ready'
WHERE processing_stage = 'indexed';

UPDATE catalog.books
SET processing_failure_category = CASE processing_failure_category
    WHEN 'manifest_integrity' THEN 'source_integrity_mismatch'
    WHEN 'incompatible_profile' THEN 'internal_processing_error'
    WHEN 'embedding_unavailable' THEN 'dependency_unavailable'
    WHEN 'vector_store_unavailable' THEN 'dependency_unavailable'
    WHEN 'indexing_timeout' THEN 'processing_timeout'
    WHEN 'internal_indexing_error' THEN 'internal_processing_error'
    ELSE processing_failure_category
END;

ALTER TABLE catalog.processing_inbox
    DROP CONSTRAINT processing_inbox_event_type_check;

ALTER TABLE catalog.processing_inbox
    ADD CONSTRAINT processing_inbox_event_type_check CHECK (event_type IN (
        'ingestion.book.processing-started.v1',
        'ingestion.book.chunks-ready.v1',
        'ingestion.book.processing-failed.v1'
    ));

ALTER TABLE catalog.books
    DROP CONSTRAINT books_processing_stage_check,
    DROP CONSTRAINT books_processing_failure_category_check;

ALTER TABLE catalog.books
    ADD CONSTRAINT books_processing_stage_check CHECK (
        processing_stage IN ('queued', 'extracting', 'chunks_ready', 'failed')
    ),
    ADD CONSTRAINT books_processing_failure_category_check CHECK (
        processing_failure_category IN ('', 'encrypted_document', 'extraction_not_permitted',
            'malformed_document', 'unsupported_document', 'no_extractable_text',
            'resource_limit_exceeded', 'source_integrity_mismatch', 'processing_timeout',
            'dependency_unavailable', 'internal_processing_error')
    );
