FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /auth .

FROM gcr.io/distroless/static-debian12
COPY --from=build /auth /auth
EXPOSE 3000
ENTRYPOINT ["/auth"]
