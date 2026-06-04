FROM oven/bun:1 AS build
WORKDIR /app
COPY package.json ./
RUN bun install
COPY . .

FROM oven/bun:1
WORKDIR /app
COPY --from=build /app /app
ENV SESSION_DIR=/data/baileys
EXPOSE 8080
CMD ["bun", "run", "src/index.ts"]
