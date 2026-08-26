set shell := ["bash", "-euo", "pipefail", "-c"]

frontend:
    cd internal/controlplane/web && npm ci && npm run build

build: frontend
    mkdir -p bin
    go build -o bin/machinist ./cmd/machinist

test:
    go test -race ./...

format-check:
    files="$(gofmt -l cmd internal)"; test -z "$files" || { printf '%s\n' "$files"; exit 1; }

check:
    cd internal/controlplane/web && npm ci && npm test && npm run build
    just format-check
    go vet ./...
    go test -race ./...
    go build ./...
