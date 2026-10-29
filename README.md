# Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_URL` | Required | PostgreSQL connection URL. |
| `LISTEN_ADDRESS` | `127.0.0.1:8080` | Backend HTTP listener behind an HTTPS proxy. |
| `PUBLIC_ORIGIN` | `https://localhost:8080` | Exact browser-facing HTTPS origin, without a path or trailing slash. |
| `OTP_HMAC_SECRET` | Required | Base64 encoding of at least 32 random bytes; shared across instances. |
| `OTP_TTL` | `10m` | Email code and login-challenge lifetime. |
| `OTP_MAX_ATTEMPTS` | `5` | Maximum failed code checks. |
| `OTP_RESEND_INTERVAL` | `1m` | Code regeneration cooldown. |
| `OTP_LOCKOUT` | `15m` | Exhausted-attempt lockout, with an effective minimum of 15 minutes. |
| `SESSION_TTL` | `720h` | Session and cookie lifetime. |
| `REQUEST_TIMEOUT` | `10s` | HTTP read/write timeout and request-context deadline. |
| `SMTP_TIMEOUT` | `10s` | SMTP operation timeout, also bounded by the request deadline. |
| `RATE_WINDOW` | `1m` | Shared per-IP rate-limit window. |
| `RATE_LIMIT` | `30` | Requests allowed per IP per bucket per window. |
| `SLOT_STEP` | `15m` | Available-slot grid spacing. |
| `TRUSTED_PROXIES` | Empty | Comma-separated proxy IPs/CIDRs trusted for `X-Forwarded-For`. |
| `SMTP_ADDRESS` | Required | SMTP `host:port`; implicit TLS on 465, mandatory STARTTLS otherwise. |
| `SMTP_FROM` | Required | Sender mailbox, optionally with a display name. |
| `SMTP_USERNAME` | Empty | SMTP username; set together with the password or leave both empty. |
| `SMTP_PASSWORD` | Empty | SMTP password. |
| `DATABASE_TEST_URL` | Empty | Disposable PostgreSQL database for integration tests that create/drop isolated schemas. |
