FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY packages/golog ./packages/golog
COPY apps/bot-go/go.mod apps/bot-go/go.sum ./apps/bot-go/
WORKDIR /src/apps/bot-go
RUN go mod download
COPY apps/bot-go/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /bot .

FROM gcr.io/distroless/static-debian12
COPY --from=builder /bot /bot
EXPOSE 3000
ENTRYPOINT ["/bot"]
