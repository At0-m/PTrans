# PTrans

PTrans - сервис обработки платежей на Go.

В проекте реализованы платежи, возвраты, доставка webhook-событий, фоновые worker-процессы и reconciliation с внешним провайдером.

## Стек

- Go
- PostgreSQL
- Docker
- JWT
- REST API

## Возможности

- Платежи и возвраты
- JWT-аутентификация
- Идемпотентные операции
- State machine для возвратов
- Transactional outbox
- Доставка webhook-событий
- Retry с backoff
- Reconciliation
- Обработка зависших возвратов
- История доставки webhook

## Запуск

```bash
cp .env.example .env
docker compose up --build