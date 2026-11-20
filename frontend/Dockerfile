# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,id=barone-npm,target=/root/.npm,sharing=locked \
    npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM caddy:2-alpine
COPY deploy/caddy/Caddyfile /etc/caddy/Caddyfile
COPY deploy/caddy/routes.caddy /etc/caddy/routes.caddy
COPY --from=build /src/frontend/dist /srv
