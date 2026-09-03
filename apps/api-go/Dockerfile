FROM golang:1.25-alpine AS build
# Contexto de build = RAIZ do repo (o serviço usa o pacote compartilhado
# github.com/santos-tech/golog via `replace ../../packages/golog`).
WORKDIR /src
COPY packages/golog ./packages/golog
COPY apps/api-go/go.mod apps/api-go/go.sum ./apps/api-go/
WORKDIR /src/apps/api-go
RUN go mod download
COPY apps/api-go/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /auth .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /auth /auth
EXPOSE 3000
ENTRYPOINT ["/auth"]
