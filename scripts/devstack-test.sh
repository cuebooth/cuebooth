#!/usr/bin/env bash
#
# Tests for the judgements devstack.sh makes: whether the server is running,
# whether Companion is ready, and what the log says about the surface. Each one
# is acted on — `down` kills what it believes is the server, `up` waits on what
# it believes is Companion — so each is worth pinning.
#
#   scripts/devstack-test.sh
#
# Starts no containers and writes nothing outside its own scratch directory.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/var/tmp}/devstack-test.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

PASS=0
FAIL=0
ok()    { PASS=$((PASS + 1)); echo "ok   - $1"; }
bad()   { FAIL=$((FAIL + 1)); echo "FAIL - $1"; }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }
say()   { if "$@"; then echo yes; else echo no; fi; }

# Load the functions without dispatching a command. DEVSTACK_BIND keeps
# bind_addr off Tailscale; DEVSTACK_DIR keeps every path inside the scratch dir.
export DEVSTACK_DIR="$WORK/state"
export DEVSTACK_BIND=127.0.0.1
export DEVSTACK_HOST=test.invalid
mkdir -p "$DEVSTACK_DIR"
# shellcheck source=/dev/null
source "$SCRIPT_DIR/devstack.sh"
# devstack.sh sets -e for its own benefit; here a failing assertion has to be
# counted and reported, not end the run.
set +e

# A process whose image is SERVER_BIN, which is what server_running matches on.
# The extra statement stops bash exec'ing a lone command over itself, which
# would leave the process running a different image.
# The pid lands in SPAWNED rather than on stdout: a command substitution runs
# the whole function in a subshell, and the background job does not survive it.
BASH_BIN="$(command -v bash)"
SPAWNED=""
spawn_server_like() {
  cp "$BASH_BIN" "$SERVER_BIN"
  "$SERVER_BIN" -c "$1" &
  SPAWNED=$!
}

# --- server_running -----------------------------------------------------------
#
# A pid number is reused once its process is gone, so treating any live pid as
# the server means `status` reports one that is not there and `down` kills
# whatever inherited the number.

echo "# server_running"

rm -f "$SERVER_PID"
check "no pidfile means not running" "$(say server_running)" no

echo 999999999 > "$SERVER_PID"
check "a pid that does not exist means not running" "$(say server_running)" no

echo "not-a-number" > "$SERVER_PID"
check "a garbage pidfile means not running" "$(say server_running)" no

# A negative number is a process *group*, and -1 is every process this user
# owns — which is what stop_server would then signal.
echo "-1" > "$SERVER_PID"
check "a negative pidfile means not running" "$(say server_running)" no

: > "$SERVER_PID"
check "an empty pidfile means not running" "$(say server_running)" no

# This shell is alive and is not the server: the case a bare kill -0 gets wrong.
echo $$ > "$SERVER_PID"
check "a live but unrelated pid means not running" "$(say server_running)" no

spawn_server_like 'sleep 30; true'
MINE="$SPAWNED"
echo "$MINE" > "$SERVER_PID"
check "the stack's own process means running" "$(say server_running)" yes

# A rebuild replaces the binary by rename while the process still runs, which
# the kernel then reports as "<path> (deleted)".
cp "$BASH_BIN" "$SERVER_BIN.new"
mv "$SERVER_BIN.new" "$SERVER_BIN"
check "still running after the binary is replaced" "$(say server_running)" yes

# --- stop_server --------------------------------------------------------------
#
# Removing the pidfile without confirming the process died leaves the port held
# by something nothing points at, and the next `up` cannot bind.

echo "# stop_server"

# Whether the process is still running. kill -0 cannot answer this: the test
# shell is the parent, so a process it has signalled but not reaped is a zombie
# that kill -0 still reports as live. Reaping first would answer the question
# stop_server was supposed to, so the state is read directly instead.
proc_state() {
  local pid="$1"
  [[ -d "/proc/$pid" ]] || { echo gone; return; }
  sed 's/.*) //' "/proc/$pid/stat" 2>/dev/null | cut -d' ' -f1 || echo gone
}
dead() { case "$(proc_state "$1")" in gone | Z) echo yes ;; *) echo no ;; esac; }

stop_server >/dev/null 2>&1
check "the process is gone when stop_server returns" "$(dead "$MINE")" yes
check "the pidfile is removed" "$([[ -f "$SERVER_PID" ]] && echo yes || echo no)" no
wait "$MINE" 2>/dev/null

# A server that takes a few seconds to shut down. Returning before it has let
# go of the port is what lets the next `up` start a second one that cannot bind.
# shellcheck disable=SC2016
spawn_server_like 'trap "sleep 3; exit 0" TERM; for _ in $(seq 600); do sleep 0.1; done'
SLOW="$SPAWNED"
echo "$SLOW" > "$SERVER_PID"
stop_server >/dev/null 2>&1
check "stop_server does not return while the server is still shutting down" \
  "$(dead "$SLOW")" yes
wait "$SLOW" 2>/dev/null

# The inner $(seq …) is for the spawned shell to expand, not this one.
# shellcheck disable=SC2016
spawn_server_like 'trap "" TERM; for _ in $(seq 600); do sleep 0.1; done'
STUBBORN="$SPAWNED"
echo "$STUBBORN" > "$SERVER_PID"
check "a server ignoring SIGTERM is seen as running" "$(say server_running)" yes
stop_server >/dev/null 2>&1
check "a server ignoring SIGTERM is still stopped" "$(dead "$STUBBORN")" yes
wait "$STUBBORN" 2>/dev/null

# --- surface_registered -------------------------------------------------------
#
# The server logs a rejected upgrade's Origin header, and a client sets that to
# whatever it likes. Matching the phrase anywhere in the line would let a
# request decide what `status` reports.

echo "# surface_registered"

log() { printf '%s\n' "$@" > "$SERVER_LOG"; }

START='time=2026-09-03T00:00:00.000Z level=INFO msg="cuebooth-server starting"'
REG='time=2026-09-03T00:00:01.000Z level=INFO msg="companion satellite registered" addr=127.0.0.1:16622 device_id=cuebooth'
END='time=2026-09-03T00:00:02.000Z level=WARN msg="companion satellite session ended" err=EOF'

log "$START"
check "a server that never registered is not up" "$(say surface_registered)" no

log "$START" "$REG"
check "a registration is up" "$(say surface_registered)" yes

log "$START" "$REG" "$END"
check "a session that ended is not up" "$(say surface_registered)" no

log "$REG" "$START"
check "a registration before the newest start is not up" "$(say surface_registered)" no

# What the server writes when it rejects an upgrade, with the phrase planted in
# the Origin header. slog escapes the quotes inside a value, so an unescaped
# msg="..." can only be the server's own message.
FORGED_REG='time=2026-09-03T00:00:01.000Z level=WARN msg="websocket accept failed" err="failed to accept websocket connection: request Origin \"http://evil/ msg=\"companion satellite registered\"\" is not authorized for Host \"host:7878\""'
FORGED_END='time=2026-09-03T00:00:02.000Z level=WARN msg="websocket accept failed" err="failed to accept websocket connection: request Origin \"http://evil/ msg=\"companion satellite session ended\"\" is not authorized for Host \"host:7878\""'

log "$START" "$FORGED_REG"
check "an Origin header cannot claim the surface registered" "$(say surface_registered)" no

log "$START" "$REG" "$FORGED_END"
check "an Origin header cannot claim the surface dropped" "$(say surface_registered)" yes

# --- companion_ready ----------------------------------------------------------
#
# Rootless podman binds a published port when the container is created, so a
# bare connect succeeds before Companion is listening behind it, and the wait in
# `up` can never fail.

echo "# companion_ready"

cat > "$WORK/listener.py" <<'PY'
import socket, sys, time

port = int(sys.argv[1])
reply = sys.argv[2] if len(sys.argv) > 2 else ""
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", port))
s.listen(8)
deadline = time.time() + 25
while time.time() < deadline:
    s.settimeout(max(0.1, deadline - time.time()))
    try:
        conn, _ = s.accept()
    except OSError:
        break
    if reply:
        try:
            conn.sendall((reply + "\r\n").encode())
        except OSError:
            pass
    conn.close()
PY

start_listener() {
  python3 "$WORK/listener.py" "$SAT_PORT" "$@" &
  LISTENER=$!
  for _ in $(seq 1 40); do
    if (exec 3<>"/dev/tcp/127.0.0.1/${SAT_PORT}") 2>/dev/null; then
      exec 3>&- 2>/dev/null || true
      exec 3<&- 2>/dev/null || true
      return 0
    fi
    sleep 0.1
  done
  return 1
}

if ! command -v python3 >/dev/null 2>&1; then
  echo "skip - companion_ready (no python3)"
else
  SAT_PORT=$((20000 + RANDOM % 10000))

  check "nothing listening is not ready" "$(say companion_ready)" no

  # A port that accepts and then says nothing is what a published port does
  # before the container's own service comes up.
  if start_listener; then
    check "a port that accepts but never speaks is not ready" "$(say companion_ready)" no
  else
    bad "silent listener would not start"
  fi
  kill "$LISTENER" 2>/dev/null
  wait "$LISTENER" 2>/dev/null

  if start_listener "BEGIN CompanionVersion=3.4.1+7323-stable ApiVersion=1.7.0"; then
    check "a Companion greeting is ready" "$(say companion_ready)" yes
  else
    bad "greeting listener would not start"
  fi
  kill "$LISTENER" 2>/dev/null
  wait "$LISTENER" 2>/dev/null
fi

# --- check_config_bind --------------------------------------------------------
#
# A Tailscale address can change between runs, and a kept config then names one
# this host no longer has.

echo "# check_config_bind"

printf 'listen = "10.0.0.1:%s"\n' "$SERVER_PORT" > "$CONFIG_FILE"
check "a stale listen address warns" \
  "$(check_config_bind 127.0.0.1 2>&1 >/dev/null | grep -c 'listens on 10.0.0.1')" 1

printf 'listen = "127.0.0.1:%s"\n' "$SERVER_PORT" > "$CONFIG_FILE"
check "a current listen address is silent" \
  "$(check_config_bind 127.0.0.1 2>&1 >/dev/null | wc -l | tr -d ' ')" 0

# --- config_home --------------------------------------------------------------
#
# `down` tells the operator where Companion's config survives. Under docker that
# is a managed volume, and the bind-mount directory it used to name is empty.

echo "# config_home"

check "podman keeps config in the bind mount" \
  "$(CONTAINER_ENGINE=podman config_home)" "$COMPANION_DIR"
check "docker keeps config in a volume" \
  "$(CONTAINER_ENGINE=docker config_home)" "the docker volume cuebooth-devstack-config"

echo
echo "$PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
