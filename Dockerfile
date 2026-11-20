# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,id=barone-go-mod,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN --mount=type=cache,id=barone-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=barone-go-build,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app
COPY --from=build /out/server /usr/local/bin/server
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
CMD ["serve"]
