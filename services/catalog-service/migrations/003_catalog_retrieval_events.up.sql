ALTER TABLE catalog.books
    DROP CONSTRAINT books_processing_stage_check,
    DROP CONSTRAINT books_processing_failure_category_check;

ALTER TABLE catalog.books
    ADD CONSTRAINT books_processing_stage_check CHECK (
        processing_stage IN ('queued', 'extracting', 'chunks_ready', 'indexed', 'failed')
    ),
    ADD CONSTRAINT books_processing_failure_category_check CHECK (
        processing_failure_category IN ('', 'encrypted_document', 'extraction_not_permitted',
            'malformed_document', 'unsupported_document', 'no_extractable_text',
            'resource_limit_exceeded', 'source_integrity_mismatch', 'processing_timeout',
            'dependency_unavailable', 'internal_processing_error', 'manifest_integrity',
            'incompatible_profile', 'embedding_unavailable', 'vector_store_unavailable',
            'indexing_timeout', 'internal_indexing_error')
    );

ALTER TABLE catalog.processing_inbox
    DROP CONSTRAINT processing_inbox_event_type_check;

ALTER TABLE catalog.processing_inbox
    ADD CONSTRAINT processing_inbox_event_type_check CHECK (event_type IN (
        'ingestion.book.processing-started.v1',
        'ingestion.book.chunks-ready.v1',
        'ingestion.book.processing-failed.v1',
        'retrieval.book.indexed.v1',
        'retrieval.book.indexing-failed.v1'
    ));
