TEST_DB_PORT ?= 55432
TEST_DATABASE_URL ?= postgres://cuevision:cuevision@127.0.0.1:$(TEST_DB_PORT)/cuevision?sslmode=disable

.PHONY: build run fmt vet test test-db test-db-down up down

build:
	CGO_ENABLED=0 go build -trimpath -o bin/cue-vision ./cmd/cue-vision

run:
	go run ./cmd/cue-vision

fmt:
	gofmt -w .

vet:
	go vet ./...

# Тесты с базой требуют TEST_DATABASE_URL; без неё они пропускаются.
test:
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -count=1 ./...

# Отдельный PostgreSQL для тестов: каждый тест работает в собственной схеме.
test-db:
	docker run -d --name cuevision-test-pg \
		-e POSTGRES_USER=cuevision -e POSTGRES_PASSWORD=cuevision -e POSTGRES_DB=cuevision \
		-p 127.0.0.1:$(TEST_DB_PORT):5432 postgres:16-alpine

test-db-down:
	docker rm -f cuevision-test-pg

up:
	docker compose up -d --build

down:
	docker compose down
