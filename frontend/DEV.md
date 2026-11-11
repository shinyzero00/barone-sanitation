# Local development

Use Node.js 22+ and npm. From `frontend/`, run `npm ci`. Create a local certificate (or use mkcert):

```sh
mkdir -p .tls
openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=localhost \
  -addext subjectAltName=DNS:localhost -keyout .tls/localhost.key -out .tls/localhost.crt
```

Trust the development certificate locally. Run Go with `PUBLIC_ORIGIN=https://localhost:5173` and `OTP_PROVIDER=console` against the Compose PostgreSQL database. Run `npm run dev` and open `https://localhost:5173`. Vite proxies to `http://127.0.0.1:8080` (`API_TARGET` overrides this). Production needs an HTTPS server with the same API routes and a SPA fallback for UI URLs; Vite's dev server is not a production proxy.

Run `npm run api:generate` against the running local Go backend after contract changes. Run `npm run build` to type-check and build production assets.
