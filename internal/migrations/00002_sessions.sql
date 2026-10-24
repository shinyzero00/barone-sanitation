-- +goose Up
CREATE TABLE sessions (
    session_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users,
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expiry ON sessions (expires_at);
CREATE TABLE ip_rate_limits (
    ip_rate_limit_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    requested_ip inet NOT NULL,
    bucket text NOT NULL,
    window_started_at timestamptz NOT NULL,
    request_count integer NOT NULL CHECK (request_count > 0),
    UNIQUE (requested_ip, bucket)
);

-- +goose Down
DROP TABLE ip_rate_limits, sessions;
