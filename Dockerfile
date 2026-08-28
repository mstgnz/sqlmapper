# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Cache the module download layer separately from the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The library is pure Go: no cgo, no sqlite client, no compiler toolchain in the
# image. Building the module root would produce an ar archive rather than a
# binary, so the command package is named explicitly.
ARG VERSION=docker

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/sqlmapper ./cmd/sqlmapper

# Final stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 sqlmapper

WORKDIR /work

COPY --from=builder /out/sqlmapper /usr/local/bin/sqlmapper
COPY --from=builder /app/examples /opt/sqlmapper/examples

USER sqlmapper

# Mount the dump into /work and let the converter write beside it:
#   docker run --rm -v "$PWD:/work" sqlmapper --file=dump.sql --to=postgres
ENTRYPOINT ["sqlmapper"]
CMD ["--help"]
