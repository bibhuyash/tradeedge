.PHONY: build test lint format

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

format:
	gofmt -w cmd internal
