# Docker Compose

Requires Docker with BuildKit and Compose 2.24.4+. Run commands from the repository root.

## Configuration

Copy `.env.compose.example` to `.env.compose`. Generate `OTP_HMAC_SECRET` with `openssl rand -base64 32` and a database password with `openssl rand -hex 32`.

An existing PostgreSQL volume keeps its original credentials: setting `POSTGRES_PASSWORD` does not rotate them. For the existing development volume, use its current password. Do not remove the volume to change credentials.

The API connects to the internal PostgreSQL service using `PGUSER`, `PGDATABASE` and `PGPASSWORD`; a host `DATABASE_URL` is not passed into the container. `PUBLIC_ORIGIN` is derived from `CADDY_DOMAIN`, which must be a hostname without a scheme, path or port.

## Let's Encrypt

Set `CADDY_DOMAIN` to your real hostname, `ACME_EMAIL` to your contact address, and configure SMTP in `.env.compose`. Point the hostname's DNS at this server and allow inbound TCP 80/443. UDP 443 enables HTTP/3. Set `ACME_CA` to the staging URL in the example file when testing ACME issuance.

```sh
docker compose --env-file .env.compose up -d --build
```

The migration container must finish successfully before the API starts; Caddy waits for the API health check. Only Caddy publishes ports. Certificates and ACME state persist in `caddy_data`; keep that volume across restarts.

## Mounted Development Certificates

Set `CADDY_DOMAIN=localhost`, configure `OTP_HMAC_SECRET`, and point `TLS_CERT_FILE` and `TLS_KEY_FILE` at your existing certificate and private key. Defaults match `frontend/.tls/localhost.crt` and `localhost.key`; see [certificate creation](../frontend/DEV.md#local-development). The certificate must cover `CADDY_DOMAIN` and be trusted by your browser.

```sh
docker compose --env-file .env.compose -f compose.yaml -f compose.dev.yaml up -d --build
```

Open `https://localhost`. This override mounts both files read-only, bypasses ACME, selects console OTP delivery and binds ports to loopback. PostgreSQL is also available on `127.0.0.1:5432`; its default development password is `sanitation` if unset. The UI is still a production build. For live Vue edits, use the existing [Vite workflow](../frontend/DEV.md).

To run only PostgreSQL for host development, use the same command with `up -d postgres` instead of `up -d --build`.

## Operations

```sh
docker compose --env-file .env.compose logs -f backend migrate caddy
docker compose --env-file .env.compose run --rm backend seed demo
docker compose --env-file .env.compose run --rm backend admin user@example.com
docker compose --env-file .env.compose down
```

Include both `-f` options for the development stack. `down` keeps data; `down -v` deletes the database and certificates. Demo seeding is explicit, never part of startup. See [seed details](../frontend/DEV.md#demo-catalog) for portraits and rerun behavior.

Go modules, Go compilation and npm downloads use BuildKit cache mounts. No development SDK or npm dependencies are included in runtime images. Secrets, local TLS keys, Git history, `node_modules` and host build artifacts are excluded from build contexts. After source changes, run `up -d --build` again.

The API trusts only Caddy's fixed address on the proxy network. If `172.30.240.0/24` overlaps a network on your host, set `CADDY_SUBNET` and a matching free `CADDY_IP` together in `.env.compose`.
