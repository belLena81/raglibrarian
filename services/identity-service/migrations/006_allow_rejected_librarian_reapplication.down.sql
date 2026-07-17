CREATE OR REPLACE FUNCTION identity.protect_user_review_fields()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, identity
AS $function$
BEGIN
    IF OLD.reviewed_by IS NOT NULL AND
       (NEW.reviewed_by IS DISTINCT FROM OLD.reviewed_by OR
        NEW.reviewed_at IS DISTINCT FROM OLD.reviewed_at) THEN
        RAISE EXCEPTION 'identity review fields are immutable';
    END IF;

    IF OLD.status <> 'pending' AND NEW.status IS DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION 'identity decision is final';
    END IF;

    RETURN NEW;
END
$function$;

CREATE OR REPLACE FUNCTION identity.notify_pending_librarians_changed()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, identity
AS $function$
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.role = 'librarian' AND NEW.status = 'pending') OR
       (TG_OP = 'UPDATE' AND OLD.role = 'librarian' AND OLD.status = 'pending' AND NEW.status <> 'pending') THEN
        PERFORM pg_notify('identity_pending_librarians_changed', '{"version":1}');
    END IF;

    RETURN NEW;
END
$function$;
