FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o /out/server ./cmd/server
RUN go build -o /out/race-router ./cmd/race-router
RUN go build -o /out/consumer ./cmd/consumer

FROM alpine:latest

WORKDIR /app

COPY --from=builder /out/server /app/server
COPY --from=builder /out/race-router /app/race-router
COPY --from=builder /out/consumer /app/consumer
COPY migrations /app/migrations

CMD ["/app/server"]
