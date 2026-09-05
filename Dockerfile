FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out && for item in payment:ptrans-api worker:ptrans-worker mockwebhook:ptrans-mock-webhook refundworker:ptrans-refund-worker mockprovider:ptrans-mock-provider token:ptrans-token seed:ptrans-seed; do \
    pkg=${item%%:*}; name=${item#*:}; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "/out/$name" "./cmd/$pkg" || exit 1; \
    done

FROM golang:1.25 AS test
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["go", "test", "-race", "-count=1", "-timeout=120s", "./..."]

FROM alpine:3.23
RUN apk add --no-cache ca-certificates \
    && addgroup -S ptrans && adduser -S -G ptrans ptrans \
    && mkdir -p /app /data && chown ptrans:ptrans /app /data
USER ptrans
WORKDIR /app
COPY --from=builder /out/ /usr/local/bin/
EXPOSE 8080
CMD ["/usr/local/bin/ptrans-api"]
