DELETE FROM catalog.outbox
WHERE event_type IN (
    'catalog.book.reindex-requested.v1',
    'catalog.book.deletion-requested.v1'
)
OR aggregate_id IN (
    SELECT id FROM catalog.books
    WHERE processing_status='deleted' OR media_type IS DISTINCT FROM 'application/pdf'
);

ALTER TABLE catalog.outbox DROP CONSTRAINT outbox_event_type_check;
ALTER TABLE catalog.outbox ADD CONSTRAINT outbox_event_type_check CHECK (event_type IN (
    'catalog.book.uploaded.v1',
    'catalog.book.processing-status-changed.v1'
));

DROP TABLE IF EXISTS catalog.lifecycle_inbox;
DROP TABLE IF EXISTS catalog.lifecycle_commands;

DELETE FROM catalog.books
WHERE processing_status='deleted' OR media_type IS DISTINCT FROM 'application/pdf';

ALTER TABLE catalog.books
    DROP CONSTRAINT IF EXISTS books_tombstone_shape_check,
    DROP CONSTRAINT IF EXISTS books_manifest_pair_check,
    DROP COLUMN IF EXISTS index_deleted,
    DROP COLUMN IF EXISTS artifacts_deleted,
    DROP COLUMN IF EXISTS original_deleted,
    DROP COLUMN IF EXISTS lifecycle_command_id,
    DROP COLUMN IF EXISTS manifest_sha256,
    DROP COLUMN IF EXISTS manifest_reference,
    DROP COLUMN IF EXISTS lifecycle_version,
    DROP CONSTRAINT books_media_type_check,
    ALTER COLUMN title SET NOT NULL,
    ALTER COLUMN author SET NOT NULL,
    ALTER COLUMN year SET NOT NULL,
    ALTER COLUMN tags SET NOT NULL,
    ALTER COLUMN object_reference SET NOT NULL,
    ALTER COLUMN checksum SET NOT NULL,
    ALTER COLUMN byte_size SET NOT NULL,
    ALTER COLUMN media_type SET NOT NULL,
    ALTER COLUMN actor_id SET NOT NULL,
    ALTER COLUMN processing_stage SET NOT NULL,
    ALTER COLUMN processing_failure_category SET NOT NULL,
    ADD CONSTRAINT books_media_type_check CHECK (media_type = 'application/pdf');
