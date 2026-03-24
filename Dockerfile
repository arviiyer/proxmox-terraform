# syntax=docker/dockerfile:1

# ── Stage 1: build the Go binary ──────────────────────────────────────────────
FROM golang:1.25-alpine AS builder
WORKDIR /src
# Only portal/ is needed — infra/ is bind-mounted at runtime
COPY portal/ .
RUN go build -o forge .

# ── Stage 2: pull Terraform binary from official image ────────────────────────
FROM hashicorp/terraform:1.10 AS terraform

# ── Stage 3: minimal runtime image ────────────────────────────────────────────
FROM debian:bookworm-slim

# ca-certificates required for TLS connections to Proxmox API
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder   /src/forge         /app/forge
COPY --from=terraform /bin/terraform     /usr/local/bin/terraform

WORKDIR /app
EXPOSE 8088
CMD ["/app/forge"]
