-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION reject_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'historical rows are immutable' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER booking_events_append_only
BEFORE UPDATE OR DELETE OR TRUNCATE ON consultation_booking_status_events
FOR EACH STATEMENT EXECUTE FUNCTION reject_history_mutation();
CREATE TRIGGER consultation_types_no_removal
BEFORE DELETE OR TRUNCATE ON consultation_types
FOR EACH STATEMENT EXECUTE FUNCTION reject_history_mutation();
CREATE TRIGGER consultation_offerings_no_removal
BEFORE DELETE OR TRUNCATE ON consultation_offerings
FOR EACH STATEMENT EXECUTE FUNCTION reject_history_mutation();
CREATE TRIGGER consultation_bookings_no_removal
BEFORE DELETE OR TRUNCATE ON consultation_bookings
FOR EACH STATEMENT EXECUTE FUNCTION reject_history_mutation();
-- +goose StatementBegin
CREATE FUNCTION protect_consultation_type_terms() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.consultation_type_id, NEW.title, NEW.description, NEW.duration_minutes, NEW.price)
        IS DISTINCT FROM ROW(OLD.consultation_type_id, OLD.title, OLD.description, OLD.duration_minutes, OLD.price)
        OR (NOT OLD.is_active AND NEW.is_active) THEN
        RAISE EXCEPTION 'create a replacement consultation type' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER consultation_types_immutable_terms BEFORE UPDATE ON consultation_types
FOR EACH ROW EXECUTE FUNCTION protect_consultation_type_terms();
-- +goose StatementBegin
CREATE FUNCTION protect_offering_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.consultation_offering_id, NEW.consultant_id, NEW.consultation_type_id)
        IS DISTINCT FROM ROW(OLD.consultation_offering_id, OLD.consultant_id, OLD.consultation_type_id) THEN
        RAISE EXCEPTION 'offering identity is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER consultation_offerings_immutable_identity BEFORE UPDATE ON consultation_offerings
FOR EACH ROW EXECUTE FUNCTION protect_offering_identity();
CREATE TRIGGER consultation_bookings_immutable BEFORE UPDATE ON consultation_bookings
FOR EACH STATEMENT EXECUTE FUNCTION reject_history_mutation();

-- +goose Down
DROP TRIGGER consultation_bookings_immutable ON consultation_bookings;
DROP TRIGGER consultation_bookings_no_removal ON consultation_bookings;
DROP TRIGGER consultation_offerings_immutable_identity ON consultation_offerings;
DROP TRIGGER consultation_types_immutable_terms ON consultation_types;
DROP TRIGGER consultation_offerings_no_removal ON consultation_offerings;
DROP TRIGGER consultation_types_no_removal ON consultation_types;
DROP TRIGGER booking_events_append_only ON consultation_booking_status_events;
DROP FUNCTION protect_offering_identity(), protect_consultation_type_terms(), reject_history_mutation();
