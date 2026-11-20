# Local development

Use Node.js 22+ and npm. From `frontend/`, run `npm ci`. Create a local certificate (or use mkcert):

```sh
mkdir -p .tls
openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=localhost \
  -addext subjectAltName=DNS:localhost -keyout .tls/localhost.key -out .tls/localhost.crt
```

Trust the development certificate locally. Run Go with `PUBLIC_ORIGIN=https://localhost:5173` and `OTP_PROVIDER=console` against the [Compose PostgreSQL database](../deploy/README.md#mounted-development-certificates). Run `npm run dev` and open `https://localhost:5173`. Vite proxies to `http://127.0.0.1:8080` (`API_TARGET` overrides this). For the built UI behind Caddy, use the [Docker stack](../deploy/README.md); Vite's dev server is not a production proxy.

Run `npm run api:generate` against the running local Go backend after contract changes. Run `npm run build` to type-check and build production assets.

## Demo catalog

From the repository root on a development database, run `go run ./cmd/server migrate up` and `go run ./cmd/server seed demo`. The seed requires an empty consultation catalog on the first run and adds weekday availability for the next two weeks in `America/New_York`. Demo consultants cannot sign in. `PRICE_CURRENCY=USD` displays the demo prices in dollars.

The three portraits in `internal/seed/assets/` are embedded in the Go binary. After replacing them, rebuild the backend and rerun `seed demo` to import any missing portraits. It does not replace existing portraits or regenerate schedules. To replace a portrait already in PostgreSQL, use `server avatar CONSULTANT_ID FILE`. Images may be JPEG, PNG, or WebP, up to 2 MiB and 2048 pixels per side.

## Browser smoke test

Run Firefox and `geckodriver --host 127.0.0.1 --port 4444` on the host. Use a disposable PostgreSQL database and point `DATABASE_URL` at it for both Go and the test. Start the backend with `OTP_PROVIDER=console` and `RATE_LIMIT=500`, redirecting stdout to a local log. With Vite running, set `UI_OTP_LOG` to the absolute log path and run `npm run test:smoke`. The test creates users and records in that database and reads OTPs only for its synthetic accounts. `UI_ORIGIN` overrides the default `https://localhost:5173`; `UI_ARTIFACTS` overrides `.artifacts/` for screenshots. Set `UI_RESEND_MS` if the backend resend interval differs from one minute.
