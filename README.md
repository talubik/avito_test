# Авито.Кухня

MVP API-маркетплейса доставки еды. Клиент выбирает заведение и позиции меню,
собирает корзину, создаёт заказ и следит за его статусом. Подключённое заведение
синхронизирует меню, получает новые заказы через webhook и управляет их статусом
через partner API.

## Быстрый запуск

Требуется Docker с Compose plugin.

```bash
docker compose up --build -d
make smoke
```

После запуска:

- Kitchen API: `http://localhost:8080`;
- demo restaurant API: `http://localhost:8081`;
- Kitchen OpenAPI: [`api/openapi/kitchen.yaml`](api/openapi/kitchen.yaml);
- demo restaurant OpenAPI: [`api/openapi/demo-restaurant.yaml`](api/openapi/demo-restaurant.yaml).

Остановить окружение:

```bash
docker compose down --remove-orphans
```

Полная очистка тестовых данных выполняется явно:

```bash
docker compose down --volumes --remove-orphans
```

## Основные сценарии

### Пользователь

1. Получает список активных заведений.
2. Открывает меню выбранного заведения.
3. Добавляет доступные позиции в корзину. Одна корзина не может объединять
   разные заведения.
4. Оформляет заказ. Сервер повторно проверяет доступность и рассчитывает сумму.
5. Получает `pending_restaurant`, затем отслеживает статусы заказа.
6. Может отменить заказ, пока ресторан его не принял.

Схема пути и проблемных веток: [`diagrams/cjm/customer.puml`](diagrams/cjm/customer.puml).

### Заведение

1. Авторизуется заранее выданным API key.
2. Публикует позиции меню, используя стабильные внешние ID и возрастающую
   `revision`.
3. Получает подписанный webhook `order.created`.
4. Дедуплицирует его по `event_id`.
5. Принимает или отклоняет заказ и последовательно обновляет его статус.

Схема: [`diagrams/cjm/restaurant.puml`](diagrams/cjm/restaurant.puml).

## Пример ручного сценария

Получить заведение и меню:

```bash
curl http://localhost:8080/v1/establishments
curl 'http://localhost:8080/v1/establishments/<establishment-id>/menu'
```

Добавить позицию в корзину:

```bash
curl -X POST http://localhost:8080/v1/cart/items \
  -H 'Content-Type: application/json' \
  -H 'X-User-ID: demo-user' \
  -d '{"menu_item_id":"<menu-item-id>","quantity":1}'
```

Оформить заказ:

```bash
curl -X POST http://localhost:8080/v1/orders \
  -H 'Content-Type: application/json' \
  -H 'X-User-ID: demo-user' \
  -H 'Idempotency-Key: checkout-001' \
  -d '{
    "delivery_address":{"city":"Москва","street":"Лесная","house":"7"},
    "customer_phone":"+79990000000",
    "comment":"Позвонить перед доставкой"
  }'
```

После доставки webhook заказ появляется в demo restaurant:

```bash
curl http://localhost:8081/v1/orders
```

Ресторан принимает заказ:

```bash
curl -X POST http://localhost:8081/v1/orders/<order-id>/status \
  -H 'Content-Type: application/json' \
  -d '{"status":"accepted"}'
```

## Архитектура

Основной сервис — модульный монолит. Demo restaurant является отдельным сервисом
и не использует внутренний код или базу маркетплейса.

- `kitchen-api` обслуживает customer и partner HTTP API;
- `kitchen-worker` доставляет события из transactional outbox;
- `demo-restaurant` показывает реальную HTTP-интеграцию заведения;
- PostgreSQL хранит каталог, корзины, заказы и outbox.

C4-диаграммы:

- [Level 2 — containers](diagrams/c4/containers.puml);
- [Level 3 — components](diagrams/c4/components.puml).

### Почему модульный монолит

В MVP каталог, корзина и заказ требуют согласованных транзакций и пока не имеют
независимых требований к масштабированию. Разделение их на сетевые сервисы
добавило бы распределённые сбои, но не принесло бы бизнес-выгоды. Граница с
рестораном, напротив, является настоящей интеграционной границей и реализована
отдельным процессом.

### Надёжная доставка заказа

Заказ и `outbox_events` создаются в одной PostgreSQL-транзакции. Worker забирает
готовые записи через `FOR UPDATE SKIP LOCKED`, подписывает payload HMAC-SHA256 и
отправляет webhook. При ошибке используется exponential backoff, максимум десять
попыток. Семантика доставки — at least once, поэтому ресторан хранит обработанные
`event_id`.

HTTP-вызов ресторана никогда не выполняется внутри транзакции создания заказа.

### Владение данными

- ресторан — источник истины для исходного меню и фактической возможности
  приготовить заказ;
- Kitchen хранит поисковую проекцию меню;
- Kitchen — источник истины для корзин и жизненного цикла заказов;
- `user_id` принадлежит внешнему сервису идентификации Авито.

Пользовательская аутентификация намеренно не реализована. `X-User-ID` моделирует
идентификатор, который в production должен ставить доверенный API Gateway. Нельзя
принимать такой заголовок напрямую из публичного интернета.

## Модель данных

Миграции находятся в [`services/kitchen/migrations`](services/kitchen/migrations),
ER-диаграмма — в [`diagrams/er/schema.puml`](diagrams/er/schema.puml).

Важные решения:

- деньги хранятся целым количеством минимальных единиц валюты;
- `order_items` содержит неизменяемые snapshot названия и цены;
- `(establishment_id, external_id)` связывает проекцию с данными ресторана;
- один пользователь может иметь только одну активную корзину;
- `(user_id, idempotency_key)` защищает checkout от повторного создания заказа;
- история переходов заказа не перезаписывается.

Жизненный цикл описан в
[`diagrams/cjm/order-state.puml`](diagrams/cjm/order-state.puml).

## API и ошибки

Контракт описан в OpenAPI 3.0.3:
[`api/openapi/kitchen.yaml`](api/openapi/kitchen.yaml).

Customer API использует `X-User-ID`. Partner API использует:

```http
Authorization: Bearer <partner-api-key>
```

Checkout требует `Idempotency-Key`. Ошибки имеют совместимый с RFC 7807 формат
`application/problem+json` и стабильное поле `code`.

Тестовые секреты из Compose предназначены исключительно для локального запуска.
API key хранится в БД как SHA-256 hash. Для высокоэнтропийного случайного ключа
это позволяет проверять точное совпадение без хранения исходного секрета.

## Проверки качества

```bash
make test
make test-integration
make lint
make openapi-lint
make smoke
```

`make test-integration` поднимает отдельную PostgreSQL в `tmpfs`, проверяет цикл
миграций `up → down → up`, транзакции, конкурентную идемпотентность и HTTP API.
Рабочая база при этом не изменяется. `make smoke` проходит сквозной сценарий:
ждёт синхронизацию меню, создаёт корзину
и заказ, проверяет доставку в ресторан, принимает заказ и проверяет новый статус
в Kitchen API.

Диаграммы хранятся как код PlantUML. Для генерации SVG:

```bash
make diagrams
```

## Структура репозитория

```text
api/openapi/                 публичный контракт
diagrams/                    C4, CJM, state machine и ER
services/kitchen/            основной API и outbox worker
services/demo-restaurant/    независимый пример интеграции
tests/e2e/                   сквозной Compose-сценарий
```

## Осознанные ограничения MVP

- нет оплаты, промокодов, курьеров и географических зон доставки;
- клиент получает изменения статуса polling-запросами;
- доступность товара синхронизируется асинхронно и не является складским
  резервированием; ресторан может отклонить заказ;
- onboarding заведений выполняется миграцией, административного API нет;
- demo restaurant хранит состояние в памяти, поэтому после рестарта получает
  новые события, но теряет старые локальные заказы;
- автоматическая отмена заказа по timeout обозначена в CJM, но в первой версии
  требует отдельной фоновой задачи;
- offset/cursor pagination и полнотекстовый поиск целесообразно добавить после
  появления продуктовых требований и объёма данных.

План, по которому построен проект, сохранён в [`PLAN.md`](PLAN.md).
