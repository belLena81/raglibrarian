DROP TABLE IF EXISTS identity.email_outbox;
DROP TABLE IF EXISTS identity.registration_verifications;
DROP TABLE IF EXISTS identity.refresh_tokens;
DROP TABLE IF EXISTS identity.sessions;
DROP TABLE IF EXISTS identity.users;

DROP FUNCTION IF EXISTS identity.notify_pending_librarians_changed();
DROP FUNCTION IF EXISTS identity.protect_user_review_fields();
