APP_URL ?= http://localhost:8080
USER_ID ?= alice
IDEMPOTENCY_KEY ?= order-1

.PHONY: test build docker-build compose-up compose-down compose-reset logs api-logs worker-logs demo-payment demo-subscription demo cancel-payment ready live

test:
	go test ./...

build:
	go build ./cmd/payment
	go build ./cmd/worker
	go build ./cmd/mockwebhook

docker-build:
	docker build -t ptrans:local .

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down

compose-reset:
	docker compose down -v
	docker compose up --build -d

logs:
	docker compose logs -f

api-logs:
	docker compose logs -f app

worker-logs:
	docker compose logs -f worker

ready:
	curl -fsS $(APP_URL)/readyz

live:
	curl -fsS $(APP_URL)/livez

demo-subscription:
	./scripts/create-subscription.sh $(APP_URL) $(USER_ID) http://mock-webhook:8090/hook

demo-payment:
	./scripts/create-payment.sh $(APP_URL) $(USER_ID) $(IDEMPOTENCY_KEY)

demo: demo-subscription demo-payment

cancel-payment:
	./scripts/cancel-payment.sh $(APP_URL) $(USER_ID) $(PAYMENT_ID)
