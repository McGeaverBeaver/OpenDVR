VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build web test run docker clean

all: web build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/opendvr ./cmd/server

web:
	cd web && npm ci --no-audit --no-fund && npm run build

test:
	go vet ./...
	go test ./...
	cd web && npm run typecheck

# Run the backend locally against a real tuner: make run HDHR_HOSTS=192.168.1.50
run: build
	HDHR_HOSTS=$(HDHR_HOSTS) DATA_DIR=./data WEB_DIR=./web/dist LOG_LEVEL=debug ./bin/opendvr

docker:
	docker build --build-arg VERSION=$(VERSION) -t ghcr.io/mcgeaverbeaver/opendvr:$(VERSION) .

clean:
	rm -rf bin web/dist data/buffer
