DELETE FROM catalog.outbox WHERE event_type = 'catalog.book.processing-status-changed.v1';
ALTER TABLE catalog.outbox DROP CONSTRAINT outbox_event_type_check;
ALTER TABLE catalog.outbox ADD CONSTRAINT outbox_event_type_check CHECK (event_type = 'catalog.book.uploaded.v1');

DROP TABLE IF EXISTS catalog.processing_inbox;

ALTER TABLE catalog.books
    DROP COLUMN processing_version,
    DROP COLUMN processing_updated_at,
    DROP COLUMN processing_failure_category,
    DROP COLUMN processing_stage;
