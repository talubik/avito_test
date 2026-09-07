.PHONY: up down logs test test-integration test-all lint openapi-lint fmt smoke diagrams

up:
	docker compose up --build -d

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f

test:
	docker run --rm -v "$(CURDIR)/services/kitchen:/src" -w /src golang:1.27-alpine go test ./...
	docker run --rm -v "$(CURDIR)/services/demo-restaurant:/src" -w /src golang:1.27-alpine go test ./...

test-integration:
	docker compose --profile test rm -sf postgres-test migrate-test kitchen-tests
	docker compose --profile test up -d postgres-test
	docker compose --profile test run --rm migrate-test
	docker compose --profile test run --rm migrate-test -path /migrations -database 'postgres://kitchen_test:kitchen_test@postgres-test:5432/kitchen_test?sslmode=disable' down 1
	docker compose --profile test run --rm migrate-test
	docker compose --profile test run --build --rm --no-deps kitchen-tests
	docker compose --profile test rm -sf postgres-test

test-all: test test-integration lint openapi-lint smoke

lint:
	docker run --rm -v "$(CURDIR)/services/kitchen:/app" -v "$(CURDIR)/.golangci.yml:/app/.golangci.yml:ro" -w /app golangci/golangci-lint:v2.13.2 golangci-lint run
	docker run --rm -v "$(CURDIR)/services/demo-restaurant:/app" -v "$(CURDIR)/.golangci.yml:/app/.golangci.yml:ro" -w /app golangci/golangci-lint:v2.13.2 golangci-lint run

openapi-lint:
	docker run --rm -v "$(CURDIR)/api/openapi:/spec:ro" redocly/cli:2.51.2 lint --config /spec/redocly.yaml /spec/kitchen.yaml /spec/demo-restaurant.yaml

fmt:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.27-alpine sh -c 'gofmt -w services'

smoke:
	bash tests/e2e/smoke.sh

diagrams:
	docker run --rm -v "$(CURDIR)/diagrams:/data" plantuml/plantuml --svg "/data/**/*.puml"
