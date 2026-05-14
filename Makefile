CGO_ENABLED ?= 0
CONFIG ?= configs/config.yaml
MOCK_CONFIG ?= configs/config.mock.yaml

.PHONY: run run-example run-mock test

run:
	CGO_ENABLED=$(CGO_ENABLED) go run ./cmd/warroom -config $(CONFIG)

run-example:
	CGO_ENABLED=$(CGO_ENABLED) go run ./cmd/warroom -config configs/config.example.yaml

run-mock:
	CGO_ENABLED=$(CGO_ENABLED) go run ./cmd/warroom -config $(MOCK_CONFIG)

test:
	CGO_ENABLED=$(CGO_ENABLED) go test ./...
