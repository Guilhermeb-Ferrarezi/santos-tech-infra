# syntax=docker/dockerfile:1

# ── build do binário Go ──────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/auto-fixer .

# ── runtime: precisa do `claude` CLI + git (o fixer roda o Claude no clone) ───
FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates ripgrep curl \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code

# usuário non-root
RUN useradd -m -u 1001 fixer
COPY --from=build /out/auto-fixer /usr/local/bin/auto-fixer

ENV WORKSPACE_ROOT=/data/workspaces
RUN mkdir -p /data/workspaces && chown -R fixer:fixer /data
USER fixer
WORKDIR /home/fixer

EXPOSE 3340
ENTRYPOINT ["auto-fixer"]
