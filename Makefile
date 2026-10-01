VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint run docker

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/compost ./cmd/compost

test:
	go test -race ./...
	php tests/php/run.php

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	for f in plugin/src/*.php; do php -l $$f >/dev/null || exit 1; done

run: build
	./bin/compost

docker:
	docker build --build-arg VERSION=$(VERSION) -t compost:$(VERSION) .
