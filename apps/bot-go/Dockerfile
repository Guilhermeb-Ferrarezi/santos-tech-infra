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
# LGPD: o golog captura request+response e redige por NOME de chave — text.body,
# from, wa_id e profile.name não casam com nenhuma regra e iriam íntegros para o
# Loki, em nível INFO, a cada mensagem de WhatsApp. O golog é compartilhado e não
# pode ser alterado aqui, então o bot-go desliga a captura de payload por padrão.
# Precisa depurar payload? Ligue LOG_BODIES=1 pontualmente, ciente do que vaza.
# (Definido na imagem porque logBodies é lido no init do golog, antes do main.)
ENV LOG_BODIES=0
EXPOSE 3000
ENTRYPOINT ["/bot"]
