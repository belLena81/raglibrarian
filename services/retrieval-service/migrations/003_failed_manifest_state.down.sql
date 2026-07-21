ALTER TABLE retrieval.manifest_facts
    DROP COLUMN IF EXISTS failure_recorded_at,
    DROP COLUMN IF EXISTS failure_category;
