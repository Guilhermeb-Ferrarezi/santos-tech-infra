FROM oven/bun:1 AS base
WORKDIR /app

COPY package.json bun.lock ./
COPY apps/api/package.json ./apps/api/
COPY packages/env/package.json ./packages/env/
COPY packages/contracts/package.json ./packages/contracts/

RUN bun install

COPY apps/api ./apps/api
COPY packages ./packages

EXPOSE 3000

CMD ["bun", "run", "apps/api/src/app.ts"]
