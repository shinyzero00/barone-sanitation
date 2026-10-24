-- +goose Up
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
CREATE TABLE verification_codes (
    verification_code_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users,
    purpose text NOT NULL CHECK (purpose IN ('verify_email', 'login', 'reset_password')),
    email text NOT NULL,
    code_hash bytea NOT NULL,
    challenge_hash bytea,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    revoked_at timestamptz,
    failed_attempts integer NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    regenerate_available_at timestamptz NOT NULL,
    requested_ip inet,
    CHECK (expires_at > created_at),
    CHECK ((purpose = 'login') = (challenge_hash IS NOT NULL))
);
CREATE INDEX verification_codes_latest ON verification_codes (user_id, purpose, verification_code_id DESC);

-- Password-only sessions must not survive the mandatory OTP transition.
UPDATE sessions SET revoked_at=clock_timestamp() WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE verification_codes;
ALTER TABLE users DROP COLUMN email_verified_at;
