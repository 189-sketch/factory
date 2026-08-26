set shell := ["bash", "-euo", "pipefail", "-c"]

frontend:
    cd internal/controlplane/web && npm ci && npm run build

build: frontend
    mkdir -p bin
    go build -o bin/factory ./cmd/factory

test:
    go test -race ./...

check:
    cd internal/controlplane/web && npm ci && npm test && npm run build
    gofmt -w cmd internal
    go vet ./...
    go test -race ./...
    go build ./...
