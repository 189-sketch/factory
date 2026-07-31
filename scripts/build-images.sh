#!/usr/bin/env bash
# Build the factory-core image chain locally: base -> codex / claude.
#
# Tag policy (C1 version lock): `latest` is for development only. Pass a
# VERSION to stamp an immutable release tag on every image; without it only
# the floating dev tags are produced.
#
#   scripts/build-images.sh                 # dev: factory-core:{base,codex,claude} (+ :latest on codex/claude)
#   VERSION=0.3.0 scripts/build-images.sh   # also tag factory-core:{codex,claude}-0.3.0 style release tags
#   PUSH=1 REGISTRY=ghcr.io/owner VERSION=0.3.0 scripts/build-images.sh   # build + push to a registry
#
# Requires a docker-compatible CLI on PATH (docker or podman).
set -euo pipefail

cd "$(dirname "$0")/.."

# ---- config ---------------------------------------------------------------
DOCKER_BIN="${DOCKER_BIN:-}"
if [[ -z "${DOCKER_BIN}" ]]; then
  if command -v docker >/dev/null 2>&1; then
    DOCKER_BIN=docker
  elif command -v podman >/dev/null 2>&1; then
    DOCKER_BIN=podman
  else
    echo "error: neither docker nor podman found on PATH" >&2
    exit 1
  fi
fi

REGISTRY="${REGISTRY:-}"          # e.g. ghcr.io/owner — empty means local-only tags
VERSION="${VERSION:-}"            # e.g. 0.3.0 — empty means dev (floating) tags only
PUSH="${PUSH:-0}"                 # 1 to push after build
IMAGE_PREFIX="factory-core"

tag() {
  # tag <name> -> fully-qualified reference honouring REGISTRY
  local name="$1"
  if [[ -n "${REGISTRY}" ]]; then
    printf '%s/%s' "${REGISTRY}" "${name}"
  else
    printf '%s' "${name}"
  fi
}

build() {
  # build <dockerfile> <image-name> [extra tags...]
  local dockerfile="$1" image="$2"
  shift 2
  local args=(--file "${dockerfile}" --tag "$(tag "${image}")")
  local extra
  for extra in "$@"; do
    args+=(--tag "$(tag "${extra}")")
  done
  echo "==> ${DOCKER_BIN} build ${args[*]} ."
  "${DOCKER_BIN}" build "${args[@]}" .
}

push() {
  # push <image-name> [extra tags...]
  [[ "${PUSH}" == "1" ]] || return 0
  local ref
  for ref in "$@"; do
    echo "==> ${DOCKER_BIN} push $(tag "${ref}")"
    "${DOCKER_BIN}" push "$(tag "${ref}")"
  done
}

# ---- build chain ----------------------------------------------------------
# base first: the provider images are FROM factory-core:base, so it must exist
# under that exact local name before they build.
echo "==> building base (toolchain image)"
build docker/Dockerfile.base "${IMAGE_PREFIX}:base"

# Provider images layer their agent CLI onto base. `latest` floats to the most
# recent dev build; a VERSION additionally stamps an immutable release tag.
codex_tags=("${IMAGE_PREFIX}:codex" "${IMAGE_PREFIX}:codex-latest")
claude_tags=("${IMAGE_PREFIX}:claude" "${IMAGE_PREFIX}:claude-latest")
if [[ -n "${VERSION}" ]]; then
  codex_tags+=("${IMAGE_PREFIX}:codex-${VERSION}")
  claude_tags+=("${IMAGE_PREFIX}:claude-${VERSION}")
fi

echo "==> building codex provider image"
build docker/Dockerfile.codex "${codex_tags[@]}"

echo "==> building claude provider image"
build docker/Dockerfile.claude "${claude_tags[@]}"

# ---- optional push --------------------------------------------------------
if [[ "${PUSH}" == "1" ]]; then
  if [[ -z "${REGISTRY}" ]]; then
    echo "error: PUSH=1 requires REGISTRY to be set" >&2
    exit 1
  fi
  push "${IMAGE_PREFIX}:base"
  push "${codex_tags[@]}"
  push "${claude_tags[@]}"
fi

echo
echo "Built images:"
"${DOCKER_BIN}" images "${IMAGE_PREFIX}" --format '  {{.Repository}}:{{.Tag}}' 2>/dev/null || true
