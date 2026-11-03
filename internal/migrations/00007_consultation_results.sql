-- +goose Up
CREATE TABLE consultation_results (
    consultation_booking_id bigint PRIMARY KEY REFERENCES consultation_bookings(consultation_booking_id),
    consultation_result text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE consultation_results;
