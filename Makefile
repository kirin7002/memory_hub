ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: db migrate seed-demo run build demo chat test test-integration fmt vet token-phone token-car
db:
	docker compose -p memoryhub -f deploy/compose.yaml up -d --wait
migrate:
	go run ./cmd/memoryhub -migrate
seed-demo:
	go run ./cmd/memoryhub -seed-demo
run:
	go run ./cmd/memoryhub
demo:
	go run ./cmd/demo
chat:
	go run ./cmd/chat $(ARGS)
build:
	go build -o bin/memoryhub ./cmd/memoryhub
test:
	go test ./...
test-integration:
	@test -n "$(TEST_DATABASE_URL)" || (echo "TEST_DATABASE_URL is required"; exit 1)
	go test -race -count=1 ./tests/integration
fmt:
	gofmt -w cmd internal migrations
vet:
	go vet ./...
token-phone:
	go run ./cmd/memoryhub -token phone
token-car:
	go run ./cmd/memoryhub -token car
