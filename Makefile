APP_URL ?= http://localhost:8080
USER_ID ?= alice

.PHONY: init test test-integration vet build docker-build compose-up compose-down logs api-logs worker-logs refund-logs ready live token seed demo demo-timeout

init:
	./scripts/init-env.sh

test:
	go test -race -count=1 ./...

test-integration:
	docker compose --profile test run --build --rm test

vet:
	go vet ./...

build:
	mkdir -p bin
	for cmd in payment worker mockwebhook refundworker mockprovider token seed; do go build -o bin/$$cmd ./cmd/$$cmd || exit 1; done

docker-build:
	docker build -t ptrans:local .

compose-up:
	docker compose up --build -d
	./scripts/wait-ready.sh $(APP_URL)

compose-down:
	docker compose --profile test down

logs:
	docker compose logs -f

api-logs:
	docker compose logs -f app

worker-logs:
	docker compose logs -f worker

refund-logs:
	docker compose logs -f refund-worker

ready:
	curl -fsS $(APP_URL)/readyz

live:
	curl -fsS $(APP_URL)/livez

token:
	docker compose exec -T app ptrans-token -user $(USER_ID)

seed:
	docker compose exec -T -e ALLOW_DEMO_SEED=true app ptrans-seed -user $(USER_ID)

demo:
	./scripts/demo-refund.sh ok

demo-timeout:
	./scripts/demo-refund.sh timeout-after-success
