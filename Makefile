GOPRIVATE ?= github.com/GoCodeAlone/*

.PHONY: build test lint clean

build:
	GOPRIVATE=$(GOPRIVATE) go build ./...

test:
	GOPRIVATE=$(GOPRIVATE) go test -race ./...

lint:
	GOPRIVATE=$(GOPRIVATE) go vet ./...

clean:
	go clean ./...
