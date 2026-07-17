CREATE OR REPLACE FUNCTION identity.protect_user_review_fields()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, identity
AS $function$
BEGIN
    IF OLD.status = 'rejected' AND NEW.status = 'pending' THEN
        IF NEW.role <> 'librarian' OR
           NEW.id IS DISTINCT FROM OLD.id OR
           NEW.email IS DISTINCT FROM OLD.email OR
           NEW.email_fingerprint IS DISTINCT FROM OLD.email_fingerprint OR
           NEW.email_verified_at IS DISTINCT FROM OLD.email_verified_at OR
           NEW.reviewed_by IS NOT NULL OR NEW.reviewed_at IS NOT NULL OR
           (to_jsonb(NEW) - ARRAY['display_name', 'password_hash', 'created_at', 'status', 'reviewed_by', 'reviewed_at'])
             IS DISTINCT FROM
           (to_jsonb(OLD) - ARRAY['display_name', 'password_hash', 'created_at', 'status', 'reviewed_by', 'reviewed_at']) THEN
            RAISE EXCEPTION 'identity rejected librarian reapplication is invalid';
        END IF;
        RETURN NEW;
    END IF;

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
       (TG_OP = 'UPDATE' AND NEW.role = 'librarian' AND NEW.status = 'pending' AND OLD.status <> 'pending') OR
       (TG_OP = 'UPDATE' AND OLD.role = 'librarian' AND OLD.status = 'pending' AND NEW.status <> 'pending') THEN
        PERFORM pg_notify('identity_pending_librarians_changed', '{"version":1}');
    END IF;

    RETURN NEW;
END
$function$;
