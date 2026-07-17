ALTER TABLE identity.email_outbox
    DROP CONSTRAINT email_outbox_message_type_check;

ALTER TABLE identity.email_outbox
    ADD CONSTRAINT email_outbox_message_type_check
    CHECK (message_type = 'verify_registration');
