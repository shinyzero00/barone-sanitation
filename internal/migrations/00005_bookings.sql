-- +goose Up
CREATE TABLE work_intervals (
    work_interval_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    consultant_id bigint NOT NULL REFERENCES consultants,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    CHECK (starts_at < ends_at)
);
CREATE INDEX work_intervals_consultant_time ON work_intervals (consultant_id, starts_at, ends_at);
CREATE TABLE consultation_bookings (
    consultation_booking_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    client_id bigint NOT NULL REFERENCES clients,
    consultation_offering_id bigint NOT NULL REFERENCES consultation_offerings,
    client_address_id bigint,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (starts_at < ends_at),
    FOREIGN KEY (client_id, client_address_id) REFERENCES client_addresses (client_id, client_address_id)
);
CREATE INDEX consultation_bookings_client_time ON consultation_bookings (client_id, starts_at);
CREATE INDEX consultation_bookings_offering_time ON consultation_bookings (consultation_offering_id, starts_at, ends_at);
CREATE INDEX consultation_bookings_address ON consultation_bookings (client_id, client_address_id);
CREATE TABLE consultation_booking_status_events (
    consultation_booking_status_event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    consultation_booking_id bigint NOT NULL REFERENCES consultation_bookings,
    status text NOT NULL CHECK (status IN ('pending', 'confirmed', 'cancelled', 'completed', 'no_show')),
    occurred_at timestamptz NOT NULL,
    user_id bigint REFERENCES users,
    reason text
);
CREATE INDEX consultation_booking_status_events_latest ON consultation_booking_status_events
    (consultation_booking_id, occurred_at DESC, consultation_booking_status_event_id DESC);
CREATE INDEX consultation_booking_status_events_user ON consultation_booking_status_events (user_id);
CREATE VIEW consultation_booking_current_statuses AS
SELECT DISTINCT ON (consultation_booking_id)
    consultation_booking_id, consultation_booking_status_event_id, status, occurred_at, user_id, reason
FROM consultation_booking_status_events
ORDER BY consultation_booking_id, occurred_at DESC, consultation_booking_status_event_id DESC;

-- +goose Down
DROP VIEW consultation_booking_current_statuses;
DROP TABLE consultation_booking_status_events, consultation_bookings, work_intervals;
