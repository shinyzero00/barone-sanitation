-- +goose Up
CREATE TABLE consultants (
    consultant_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL UNIQUE REFERENCES users,
    bio text,
    is_active boolean NOT NULL DEFAULT true
);
CREATE TABLE client_addresses (
    client_address_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    client_id bigint NOT NULL REFERENCES clients,
    address_line text NOT NULL,
    label text,
    UNIQUE (client_id, client_address_id)
);
CREATE TABLE consultation_types (
    consultation_type_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title text NOT NULL,
    description text NOT NULL,
    duration_minutes integer NOT NULL CHECK (duration_minutes > 0),
    price numeric(12,2) NOT NULL CHECK (price >= 0),
    is_active boolean NOT NULL DEFAULT true
);
CREATE TABLE consultation_offerings (
    consultation_offering_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    consultant_id bigint NOT NULL REFERENCES consultants,
    consultation_type_id bigint NOT NULL REFERENCES consultation_types,
    is_active boolean NOT NULL DEFAULT true,
    UNIQUE (consultant_id, consultation_type_id)
);
CREATE INDEX consultation_offerings_type ON consultation_offerings (consultation_type_id);

-- +goose Down
DROP TABLE consultation_offerings, consultation_types, client_addresses, consultants;
