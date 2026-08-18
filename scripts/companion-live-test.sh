#!/usr/bin/env bash
#
# Run the Satellite integration test against a real Companion.
#
#   scripts/companion-live-test.sh [version]        # default: the production-PC version
#   COMPANION_KEEP=1 scripts/companion-live-test.sh # leave Companion running afterwards
#   COMPANION_SATELLITE_ADDR=host:port scripts/companion-live-test.sh   # use an existing instance
#
# Starts the pinned Companion image, waits for its Satellite port, runs the
# `Online` tests in server/internal/companion, and tears the container down.
# CI calls this same script (see .github/workflows/companion-live.yml) so the
# local and CI paths cannot drift.
set -euo pipefail

VERSION="${1:-v3.4.1}"
IMAGE="ghcr.io/bitfocus/companion/companion:${VERSION}"
CONTAINER="cuebooth-livetest-${VERSION//[^a-zA-Z0-9]/-}"
# Non-default host ports so a local Companion on 8000/16622 isn't disturbed.
ADMIN_PORT="${COMPANION_ADMIN_PORT:-18000}"
SAT_PORT="${COMPANION_SAT_PORT:-18622}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Reuse an instance the caller already has, rather than starting one.
if [[ -n "${COMPANION_SATELLITE_ADDR:-}" ]]; then
  echo "==> using existing Companion at ${COMPANION_SATELLITE_ADDR}"
  cd "${REPO_ROOT}/server"
  COMPANION_VERSION="${VERSION}" go test ./internal/companion/ -run Online -v -count=1
  exit $?
fi

ENGINE="${CONTAINER_ENGINE:-}"
if [[ -z "$ENGINE" ]]; then
  for candidate in podman docker; do
    if command -v "$candidate" >/dev/null 2>&1; then ENGINE="$candidate"; break; fi
  done
fi
if [[ -z "$ENGINE" ]]; then
  echo "no container engine found (need podman or docker)" >&2
  exit 1
fi
echo "==> engine: $ENGINE   image: $IMAGE"

cleanup() {
  if [[ "${COMPANION_KEEP:-0}" == "1" ]]; then
    echo "==> COMPANION_KEEP=1, leaving $CONTAINER running (admin http://127.0.0.1:${ADMIN_PORT})"
    return
  fi
  echo "==> removing $CONTAINER"
  "$ENGINE" rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"$ENGINE" rm -f "$CONTAINER" >/dev/null 2>&1 || true
"$ENGINE" pull "$IMAGE"
"$ENGINE" run -d --name "$CONTAINER" \
  -p "127.0.0.1:${ADMIN_PORT}:8000" \
  -p "127.0.0.1:${SAT_PORT}:16622" \
  "$IMAGE" >/dev/null

# Companion takes a few seconds to render its first surface; wait for the
# Satellite port to accept a connection rather than sleeping a fixed amount.
echo -n "==> waiting for Satellite port ${SAT_PORT} "
for _ in $(seq 1 90); do
  if (exec 3<>"/dev/tcp/127.0.0.1/${SAT_PORT}") 2>/dev/null; then
    exec 3<&- 2>/dev/null || true
    echo " ready"
    break
  fi
  echo -n "."
  sleep 1
done
if ! (exec 3<>"/dev/tcp/127.0.0.1/${SAT_PORT}") 2>/dev/null; then
  echo
  echo "Companion did not open ${SAT_PORT} in time; container log:" >&2
  "$ENGINE" logs --tail 40 "$CONTAINER" >&2 || true
  exit 1
fi
exec 3<&- 2>/dev/null || true

cd "${REPO_ROOT}/server"
set +e
COMPANION_SATELLITE_ADDR="127.0.0.1:${SAT_PORT}" \
COMPANION_VERSION="${VERSION}" \
  go test ./internal/companion/ -run Online -v -count=1
STATUS=$?
set -e

# Companion's own log is the first thing worth seeing when the test fails, and
# the cleanup trap is about to delete the container.
if [[ $STATUS -ne 0 ]]; then
  echo "==> test failed; last 60 lines of Companion ${VERSION}:" >&2
  "$ENGINE" logs --tail 60 "$CONTAINER" >&2 || true
fi
exit $STATUS
