-- +goose Up
CREATE TABLE blobs (
    blob_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    content_type text NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 2097152),
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE clients ADD COLUMN avatar_blob_id bigint UNIQUE REFERENCES blobs;
ALTER TABLE consultants ADD COLUMN avatar_blob_id bigint UNIQUE REFERENCES blobs;

-- +goose Down
ALTER TABLE consultants DROP COLUMN avatar_blob_id;
ALTER TABLE clients DROP COLUMN avatar_blob_id;
DROP TABLE blobs;
