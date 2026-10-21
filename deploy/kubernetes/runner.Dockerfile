FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm AS build
ARG TARGETARCH
ARG VERSION=development
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-X main.version=${VERSION}" -o /machinist ./cmd/machinist

FROM golang:1.26.6-bookworm AS target-toolchain

FROM node:22.22.2-bookworm-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git curl python3 build-essential procps && rm -rf /var/lib/apt/lists/*
ARG CODEX_VERSION=0.157.1
RUN npm install -g "@openai/codex@${CODEX_VERSION}"
COPY --from=target-toolchain /usr/local/go /usr/local/go
RUN test "$(/usr/local/go/bin/go env GOHOSTARCH)" = "${TARGETARCH}"
RUN command -v ps >/dev/null && ps axeww -o pid=,command= >/dev/null
COPY --from=build /machinist /usr/local/bin/machinist
ENV PATH="/usr/local/go/bin:${PATH}" HOME=/home/agent CODEX_HOME=/home/agent/.codex GIT_TERMINAL_PROMPT=0
USER 1000:1000
WORKDIR /source
ENTRYPOINT ["machinist"]
CMD ["sandbox", "run"]
