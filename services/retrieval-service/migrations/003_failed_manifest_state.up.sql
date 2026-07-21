ALTER TABLE retrieval.manifest_facts
    ADD COLUMN failure_category text,
    ADD COLUMN failure_recorded_at timestamptz;
