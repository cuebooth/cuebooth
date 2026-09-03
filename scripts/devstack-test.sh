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

# A checkout or a DEVSTACK_DIR reached through a symlink resolves to a different
# path than the kernel reports for a running process, so comparing the two
# naively makes the stack's own server invisible to it: `up` reports a failure
# that did not happen, `status` says down, and `down` loses the handle.
echo "# server_running through a symlink"

mkdir -p "$WORK/real"
ln -sfn "$WORK/real" "$WORK/link"
SYM_OUT="$(
  DEVSTACK_DIR="$WORK/link" bash -c '
    source "'"$SCRIPT_DIR"'/devstack.sh"
    set +e
    cp "$(command -v bash)" "$SERVER_BIN"
    "$SERVER_BIN" -c "sleep 20; true" &
    pid=$!
    echo "$pid" > "$SERVER_PID"
    if server_running; then echo yes; else echo no; fi
    kill -9 "$pid" 2>/dev/null
  ' 2>/dev/null | tail -1
)"
check "the server is found through a symlinked state dir" "$SYM_OUT" yes

mkdir -p "$WORK/real-unmade"
ln -sfn "$WORK/real-unmade" "$WORK/link-unmade"
NEW_OUT="$(
  DEVSTACK_DIR="$WORK/link-unmade/state" bash -c '
    source "'"$SCRIPT_DIR"'/devstack.sh"
    set +e
    echo "$SERVER_BIN"
  ' 2>/dev/null | tail -1
)"
check "a state dir that does not exist yet still resolves physically" \
  "$NEW_OUT" "$WORK/real-unmade/state/cuebooth-server"

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

# A pidfile naming a live process that is not this stack's server is not a stale
# pidfile. Deleting it leaves that process holding the port with nothing
# pointing at it, and the next `up` cannot bind.
echo "# stop_server, unrecognised process"

# Some other build of the server, or a server started by hand: a live process
# whose image is not the binary this script builds.
cp "$BASH_BIN" "$WORK/other-cuebooth-server"
cp "$BASH_BIN" "$SERVER_BIN"
"$WORK/other-cuebooth-server" -c 'sleep 30; true' &
FOREIGN=$!
echo "$FOREIGN" > "$SERVER_PID"

check "an unrecognised live process is not the server" "$(say server_running)" no
check "it is reported as unrecognised, not absent" "$(unrecognised_server)" "$FOREIGN"
# /bin/true stands in for the container engine: it reports no container, so
# this asserts on the server line without depending on what is installed.
check "status says the pid is alive rather than just 'down'" \
  "$(CONTAINER_ENGINE=/bin/true cmd_status 2>/dev/null | grep -c "is alive and is not")" 1
OUT="$(stop_server 2>&1 >/dev/null)"
check "an unrecognised live process is left running" "$(dead "$FOREIGN")" no
check "its pidfile is kept" "$([[ -f "$SERVER_PID" ]] && echo yes || echo no)" yes
check "and stop_server says so" "$(printf '%s' "$OUT" | grep -c "leaving both alone")" 1
kill -9 "$FOREIGN" 2>/dev/null
wait "$FOREIGN" 2>/dev/null
rm -f "$SERVER_PID"

# Starting must not overwrite what stopping goes to trouble to preserve: the
# new server cannot bind the port anyway, and the pidfile was the only handle.
cp "$BASH_BIN" "$WORK/other-cuebooth-server"
"$WORK/other-cuebooth-server" -c 'sleep 30; true' &
FOREIGN2=$!
echo "$FOREIGN2" > "$SERVER_PID"

( launch_server ) >/dev/null 2>&1
check "launch_server refuses to overwrite a live foreign pidfile" \
  "$(cat "$SERVER_PID")" "$FOREIGN2"
check "and the process is untouched" "$(dead "$FOREIGN2")" no

START_OUT="$( ( start_server ) 2>&1 >/dev/null )"
check "start_server refuses too" "$(cat "$SERVER_PID")" "$FOREIGN2"
# Its own message, not launch_server's: refusing here is what saves the build.
check "and says so before building" \
  "$(printf '%s' "$START_OUT" | grep -c 'Stop it, or point')" 1
kill -9 "$FOREIGN2" 2>/dev/null
wait "$FOREIGN2" 2>/dev/null
rm -f "$SERVER_PID"

# A pidfile for a process that really is gone is stale, and goes.
echo 999999999 > "$SERVER_PID"
stop_server >/dev/null 2>&1
check "a stale pidfile is removed" "$([[ -f "$SERVER_PID" ]] && echo yes || echo no)" no

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

# surface_registered matches the server's own log messages. The tests above
# write those lines themselves, so they pin the parser without pinning the
# contract: a rename in server/ would leave every assertion here passing while
# `up` and `status` stopped seeing a registered surface.
echo "# surface_registered matches what the server actually logs"

SERVER_SRC="$SCRIPT_DIR/../server"
for msg in "cuebooth-server starting" \
           "companion satellite registered" \
           "companion satellite session ended"; do
  check "the server still logs \"$msg\"" \
    "$(grep -rlF "\"$msg\"" "$SERVER_SRC" --include='*.go' 2>/dev/null | grep -cv '_test\.go' | tr -d ' ')" 1
done

# --- usage --------------------------------------------------------------------
#
# The header block is the only documentation of the knobs, and it used to be
# extracted by hardcoded line numbers.

echo "# usage"

USAGE="$(usage)"
check "usage ends at the header, not in the code" \
  "$(printf '%s' "$USAGE" | grep -c 'set -euo pipefail')" 0
check "usage reaches the last header line" \
  "$(printf '%s' "$USAGE" | grep -c 'companion-live-test.sh')" 1
for knob in DEVSTACK_BIND DEVSTACK_DIR DEVSTACK_ADMIN_PORT DEVSTACK_SERVER_PORT CONTAINER_ENGINE; do
  check "usage documents $knob" "$(printf '%s' "$USAGE" | grep -c "$knob")" 1
done

# --- publish_host -------------------------------------------------------------

echo "# publish_host"

check "an IPv4 address is passed through" "$(publish_host 100.64.0.1)" "100.64.0.1"
check "an IPv6 literal is bracketed for -p" "$(publish_host fd7a::1)" "[fd7a::1]"

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

# --- companion_is_current -----------------------------------------------------
#
# `up` reuses a running container by name. If it came from a different
# DEVSTACK_COMPANION_VERSION, reusing it is the difference between testing an
# upgrade and believing you have.

echo "# companion_is_current"

# A stand-in engine: `ps --format '{{.Image}}'` answers from FAKE_IMAGE, and
# every other field from the container name.
FAKE_ENGINE="$WORK/bin/podman"
mkdir -p "$WORK/bin"
cat > "$FAKE_ENGINE" <<'FAKE'
#!/usr/bin/env bash
if [[ "${1:-}" == "ps" ]]; then
  [[ -n "${FAKE_IMAGE:-}" ]] || exit 0
  for arg in "$@"; do
    case "$arg" in
      '{{.Image}}') echo "$FAKE_IMAGE"; exit 0 ;;
      '{{.Names}}') echo "cuebooth-devstack"; exit 0 ;;
      '{{.Ports}}') echo "${FAKE_PORTS:-}"; exit 0 ;;
    esac
  done
fi
exit 0
FAKE
chmod +x "$FAKE_ENGINE"
export CONTAINER_ENGINE="$FAKE_ENGINE"

FAKE_IMAGE="" check "no container is not current" "$(FAKE_IMAGE="" say companion_is_current)" no
check "no container is not running" "$(FAKE_IMAGE="" say companion_running)" no
check "the wanted image is current" "$(FAKE_IMAGE="$IMAGE" say companion_is_current)" yes
check "a docker.io-prefixed name is the same image" \
  "$(FAKE_IMAGE="docker.io/$IMAGE" say companion_is_current)" yes
check "a different tag is not current" \
  "$(FAKE_IMAGE="ghcr.io/bitfocus/companion/companion:v5.0.3" say companion_is_current)" no
check "a container from another tag still counts as running" \
  "$(FAKE_IMAGE="ghcr.io/bitfocus/companion/companion:v5.0.3" say companion_running)" yes

# `up` recomputes the bind address every run; a running container keeps the one
# it was created with.
check "a container published on this address is fine" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="127.0.0.1:8000->8000/tcp, 100.64.0.1:8000->8000/tcp" say companion_publishes 100.64.0.1)" yes
check "a container published elsewhere is not" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="127.0.0.1:8000->8000/tcp, 100.64.0.9:8000->8000/tcp" say companion_publishes 100.64.0.1)" no

unset CONTAINER_ENGINE

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
