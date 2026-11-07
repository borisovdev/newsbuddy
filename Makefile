VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: test test-race cover lint fmt check-storage-boundary check-cache-boundary check-cgo check docker up down logs clean

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

lint:
	@fmtout="$$(gofmt -l .)"; \
	if [ -n "$$fmtout" ]; then \
		echo "gofmt: the following files are not formatted:"; \
		echo "$$fmtout"; \
		exit 1; \
	fi
	go vet ./...

fmt:
	gofmt -w .

check-storage-boundary:
	@out="$$(grep -rn 'database/sql\|Masterminds/squirrel' --include='*.go' --exclude='*_test.go' cmd internal | grep -v '^internal/storage/sqlite/')"; \
	if [ -n "$$out" ]; then \
		echo "$$out"; \
		echo "check-storage-boundary: forbidden storage internals leaked outside internal/storage/sqlite/"; \
		exit 1; \
	fi

check-cache-boundary:
	@out="$$(grep -rn 'sync\.Map\|map\[string\]' --include='*.go' --exclude='*_test.go' cmd internal | grep -v '^internal/cache/')"; \
	if [ -n "$$out" ]; then \
		echo "$$out"; \
		echo "check-cache-boundary: forbidden cache internals leaked outside internal/cache/"; \
		exit 1; \
	fi

check-cgo:
	CGO_ENABLED=0 go build ./...

check: lint test-race check-storage-boundary check-cache-boundary check-cgo

docker:
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t news-buddy .

up:
	VERSION=$(VERSION) $(COMPOSE) up -d --build

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f

clean:
	rm -rf coverage.out
