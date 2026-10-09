BIN := bin/itemize-ynab

.PHONY: build test lint vet cover tidy check

build:
	go build -o $(BIN) ./cmd/itemize-ynab

test:
	go test ./... -race

lint:
	golangci-lint run

vet:
	go vet ./...

cover:
	go test ./... -race -coverprofile=coverage.out
	go tool cover -func=coverage.out

tidy:
	go mod tidy

check: vet lint test
