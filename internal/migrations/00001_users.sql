-- +goose Up
CREATE TABLE users (
    user_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    email text NOT NULL,
    password_hash text NOT NULL,
    role text NOT NULL DEFAULT 'client' CHECK (role IN ('client', 'consultant', 'admin'))
);
CREATE UNIQUE INDEX users_email_ci ON users (lower(email));
CREATE TABLE clients (
    client_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL UNIQUE REFERENCES users,
    phone text NOT NULL
);

-- +goose Down
DROP TABLE clients, users;
