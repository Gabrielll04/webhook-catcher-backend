FROM golang:1.24-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/webhook-catcher ./cmd/local

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app
WORKDIR /app

COPY --from=builder /out/webhook-catcher /app/webhook-catcher
COPY --from=builder /src/migrations /app/migrations
RUN mkdir -p /app/data && chown -R app:app /app

USER app
ENV PORT=10000
EXPOSE 10000

CMD ["./webhook-catcher"]
