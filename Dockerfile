FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ptrans-api ./cmd/payment \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ptrans-worker ./cmd/worker \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ptrans-mock-webhook ./cmd/mockwebhook

FROM alpine:3.20
RUN addgroup -S ptrans && adduser -S -G ptrans ptrans
USER ptrans
WORKDIR /app
COPY --from=builder /out/ptrans-api /usr/local/bin/ptrans-api
COPY --from=builder /out/ptrans-worker /usr/local/bin/ptrans-worker
COPY --from=builder /out/ptrans-mock-webhook /usr/local/bin/ptrans-mock-webhook
EXPOSE 8080
CMD ["/usr/local/bin/ptrans-api"]
