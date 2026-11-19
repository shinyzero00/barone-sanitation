# Local development

Use Node.js 22+ and npm. From `frontend/`, run `npm ci`. Create a local certificate (or use mkcert):

```sh
mkdir -p .tls
openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=localhost \
  -addext subjectAltName=DNS:localhost -keyout .tls/localhost.key -out .tls/localhost.crt
```

Trust the development certificate locally. Run Go with `PUBLIC_ORIGIN=https://localhost:5173` and `OTP_PROVIDER=console` against the Compose PostgreSQL database. Run `npm run dev` and open `https://localhost:5173`. Vite proxies to `http://127.0.0.1:8080` (`API_TARGET` overrides this). Production needs an HTTPS server with the same API routes and a SPA fallback for UI URLs; Vite's dev server is not a production proxy.

Run `npm run api:generate` against the running local Go backend after contract changes. Run `npm run build` to type-check and build production assets.

## Browser smoke test

Run Firefox and `geckodriver --host 127.0.0.1 --port 4444` on the host. Use a disposable PostgreSQL database and point `DATABASE_URL` at it for both Go and the test. Start the backend with `OTP_PROVIDER=console` and `RATE_LIMIT=500`, redirecting stdout to a local log. With Vite running, set `UI_OTP_LOG` to the absolute log path and run `npm run test:smoke`. The test creates users and records in that database and reads OTPs only for its synthetic accounts. `UI_ORIGIN` overrides the default `https://localhost:5173`; `UI_ARTIFACTS` overrides `.artifacts/` for screenshots. Set `UI_RESEND_MS` if the backend resend interval differs from one minute.
