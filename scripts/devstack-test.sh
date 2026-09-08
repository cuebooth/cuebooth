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
# This file defines stub overrides of the sourced script's functions. The linter
# cannot see those being invoked, and which code it reports for that varies by
# version (SC2329 here, SC2317 on the CI runner), so both are off for the file.
# shellcheck disable=SC2317,SC2329
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
  "$NEW_OUT" "$(readlink -m "$WORK/real-unmade")/state/cuebooth-server"

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
SLOW_OUT="$(stop_server 2>&1)"
check "stop_server does not return while the server is still shutting down" \
  "$(dead "$SLOW")" yes
# Waiting is the point: without it the SIGKILL below would satisfy the check
# above just as well, and every `down` would become an immediate kill.
check "and lets it shut down rather than killing it" \
  "$(printf '%s' "$SLOW_OUT" | grep -c 'ignored SIGTERM')" 0
wait "$SLOW" 2>/dev/null
check "the slow server exited on its own terms" "$?" 0

# The inner $(seq …) is for the spawned shell to expand, not this one.
# shellcheck disable=SC2016
spawn_server_like 'trap "" TERM; for _ in $(seq 600); do sleep 0.1; done'
STUBBORN="$SPAWNED"
echo "$STUBBORN" > "$SERVER_PID"
check "a server ignoring SIGTERM is seen as running" "$(say server_running)" yes
STUBBORN_OUT="$(stop_server 2>&1)"
check "a server ignoring SIGTERM is still stopped" "$(dead "$STUBBORN")" yes
check "and says it had to escalate" \
  "$(printf '%s' "$STUBBORN_OUT" | grep -c 'ignored SIGTERM')" 1
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

# A pidfile holding "-1" names every process this user owns. Without the guard
# `kill -0 -1` succeeds, and `down` then refuses to stop the real server while
# `up` refuses to start, until the pidfile is deleted by hand.
for bogus in -1 0 not-a-number; do
  printf '%s\n' "$bogus" > "$SERVER_PID"
  check "a pidfile of '$bogus' is not an unrecognised server" \
    "$(unrecognised_server; echo "rc=$?")" "rc=1"
done
printf '%s\n' "$FOREIGN" > "$SERVER_PID"
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

# stop_server keeps the pidfile when a process survives SIGKILL, so a restart
# reaches launch_server with a live server of this stack's own still recorded.
# Overwriting it there loses the same handle stop_server just protected.
spawn_server_like 'sleep 30; true'
OWN="$SPAWNED"
echo "$OWN" > "$SERVER_PID"
( launch_server ) >/dev/null 2>&1
check "launch_server refuses over this stack's own running server" \
  "$(cat "$SERVER_PID")" "$OWN"
check "and leaves it running" "$(dead "$OWN")" no
kill -9 "$OWN" 2>/dev/null
wait "$OWN" 2>/dev/null
cp "$BASH_BIN" "$SERVER_BIN"
echo "$FOREIGN2" > "$SERVER_PID"

START_OUT="$( ( start_server ) 2>&1 >/dev/null )"
check "start_server refuses too" "$(cat "$SERVER_PID")" "$FOREIGN2"
# Its own message, not launch_server's: refusing here is what saves the build.
check "and says so before building" \
  "$(printf '%s' "$START_OUT" | grep -c 'delete ')" 1
kill -9 "$FOREIGN2" 2>/dev/null
wait "$FOREIGN2" 2>/dev/null
rm -f "$SERVER_PID"

# launch_server's success path. Reached only through failures until now, so
# neither the detachment nor the pid it records was pinned.
echo "# launch_server"

# The image check is stubbed because the stand-in below is a script, whose image
# the kernel reports as its interpreter. Everything else in server_running still
# runs, so launch_server's own refusal-to-overwrite guard is exercised on the
# way in. What is under test is which pid gets recorded and what session it
# lands in.
rm -f "$SERVER_PID"
printf '#!/bin/sh\nsleep 30\n' > "$SERVER_BIN"
chmod +x "$SERVER_BIN"
(
  server_pid_matches() { return 0; }
  launch_server
) >/dev/null 2>&1
LAUNCHED="$(cat "$SERVER_PID" 2>/dev/null || echo none)"

check "the pidfile names a live process" \
  "$(kill -0 "$LAUNCHED" 2>/dev/null && echo yes || echo no)" yes
# The recorded pid has to be the server, not the shell that started it.
check "and not this shell" "$([[ "$LAUNCHED" == "$$" ]] && echo yes || echo no)" no

# setsid, so the stack outlives the shell that launched it: the server must be
# in its own session, not the test shell's. That is issue #85's "the stack keeps
# running after the invoking shell exits".
session_of() { sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f4; }
check "the server is detached into its own session" \
  "$([[ "$(session_of "$LAUNCHED")" == "$(session_of $$)" ]] && echo same || echo detached)" detached

kill -9 "$LAUNCHED" 2>/dev/null
rm -f "$SERVER_PID"

# A pidfile for a process that really is gone is stale, and goes.
echo 999999999 > "$SERVER_PID"
stop_server >/dev/null 2>&1
check "a stale pidfile is removed" "$([[ -f "$SERVER_PID" ]] && echo yes || echo no)" no

# A server that dies at start — a bad config, a port already taken — must not
# leave its pid behind. Once that number is reused by anything else, `up` and
# `down` both refuse to touch it and the state dir has to be cleaned by hand.
printf '#!/bin/sh\nexit 1\n' > "$SERVER_BIN"
chmod +x "$SERVER_BIN"
( launch_server ) >/dev/null 2>&1
check "a server that dies at start leaves no pidfile" \
  "$([[ -f "$SERVER_PID" ]] && echo yes || echo no)" no

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

# What the server writes when it rejects an upgrade carrying a planted message
# field, captured from a real server rather than composed here.
#
# coder/websocket logs the *parsed host* when an Origin names one, which drops
# anything planted after it — so the reachable vectors are an Origin with no
# host at all, and one that does not parse. Both log the header verbatim. slog
# then escapes the quotes inside the value, which is what makes an unescaped
# msg="..." impossible to forge.
FORGED_REG='time=2026-09-03T07:42:00.615Z level=WARN msg="websocket accept failed" err="failed to accept WebSocket connection: request Origin \"msg=\\\"companion satellite registered\\\" device_id=cuebooth\" is not a valid URL with a host"'
FORGED_END='time=2026-09-03T07:42:00.625Z level=WARN msg="websocket accept failed" err="failed to accept WebSocket connection: failed to parse Origin header \"ht tp://x msg=\\\"companion satellite session ended\\\"\": parse \"ht tp://x\": first path segment in URL cannot contain colon"'

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
for knob in DEVSTACK_BIND DEVSTACK_DIR DEVSTACK_ADMIN_PORT DEVSTACK_SERVER_PORT \
            DEVSTACK_REGENERATE CONTAINER_ENGINE; do
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
  # Restored afterwards: every later section derives expected ports from these,
  # and a random value chosen here would silently redefine what they assert.
  REAL_SAT_PORT="$SAT_PORT"
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

  SAT_PORT="$REAL_SAT_PORT"
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

# `up` recomputes the publish list every run; a running container keeps the one
# it was created with, so reuse has to compare the whole set.
MATCHING="127.0.0.1:8000->8000/tcp, 127.0.0.1:16622->16622/tcp, 100.64.0.1:8000->8000/tcp"

check "a container publishing exactly this is fine" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$MATCHING" say companion_publishes 100.64.0.1)" yes
check "a container published on another address is not" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="127.0.0.1:8000->8000/tcp, 127.0.0.1:16622->16622/tcp, 100.64.0.9:8000->8000/tcp" say companion_publishes 100.64.0.1)" no

# Tailscale addresses differ by a trailing digit routinely, so the match has to
# end at the port separator.
check "an address that is only a prefix does not count as published" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="127.0.0.1:8000->8000/tcp, 127.0.0.1:16622->16622/tcp, 100.64.126.18:8000->8000/tcp" say companion_publishes 100.64.126.1)" no

# A container created before the Satellite port came off the tailnet publishes
# one mapping too many. Reusing it would leave that endpoint exposed for as long
# as the container lives, with nothing said about it.
check "an extra published mapping is a mismatch" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$MATCHING, 100.64.0.1:16622->16622/tcp" say companion_publishes 100.64.0.1)" no

# A port knob changed since the container was created.
check "a container on the old admin port is a mismatch" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$MATCHING" ADMIN_PORT=8999 say companion_publishes 100.64.0.1)" no
check "a container on the old satellite port is a mismatch" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$MATCHING" SAT_PORT=16999 say companion_publishes 100.64.0.1)" no

unset CONTAINER_ENGINE

# --- write_config -------------------------------------------------------------
#
# The generated file says "Edit freely", and the preset coordinates in it are
# hand-tuned to whatever page the operator built in Companion. Which way this
# decision goes is the difference between keeping those edits and discarding
# them on every run.

echo "# write_config"

printf 'hand-edited by the operator\n' > "$CONFIG_FILE"
write_config 127.0.0.1 >/dev/null 2>&1
check "an existing config is kept" \
  "$(grep -c 'hand-edited' "$CONFIG_FILE")" 1

DEVSTACK_REGENERATE=1 write_config 127.0.0.1 >/dev/null 2>&1
check "DEVSTACK_REGENERATE=1 replaces it" \
  "$(grep -c 'hand-edited' "$CONFIG_FILE")" 0
check "and what it writes is this run's listen address" \
  "$(grep -c "listen = \"127.0.0.1:${SERVER_PORT}\"" "$CONFIG_FILE")" 1

rm -f "$CONFIG_FILE"
write_config 127.0.0.1 >/dev/null 2>&1
check "a missing config is generated" \
  "$(grep -c '\[companion.satellite\]' "$CONFIG_FILE")" 1

# An IPv6 bind has to produce an address the server can parse; unbracketed,
# "fd7a::1:7878" is "too many colons".
rm -f "$CONFIG_FILE"
write_config fd7a::1 >/dev/null 2>&1
check "an IPv6 bind is bracketed in listen" \
  "$(grep -c "listen = \"\[fd7a::1\]:${SERVER_PORT}\"" "$CONFIG_FILE")" 1

# --- check_config_drift -------------------------------------------------------
#
# The config is kept across runs, so every setting this run would have written
# can disagree with what is in the file. A Tailscale address can change; so can
# a port knob, and a satellite address left pointing at 16622 sends the server
# to whatever holds that port — on a machine that already runs Companion, the
# operator's own.

echo "# check_config_drift"

write_kept_config() {
  printf 'listen = "%s"\nbase_url = "%s"\naddr = "%s"\n' "$1" "$2" "$3" > "$CONFIG_FILE"
}

current_config() {
  write_kept_config "127.0.0.1:${SERVER_PORT}" \
    "http://127.0.0.1:${ADMIN_PORT}" "127.0.0.1:${SAT_PORT}"
}

current_config
check "a config matching this run is silent" \
  "$(check_config_drift 127.0.0.1 2>&1 >/dev/null | wc -l | tr -d ' ')" 0

write_kept_config "10.0.0.1:${SERVER_PORT}" \
  "http://127.0.0.1:${ADMIN_PORT}" "127.0.0.1:${SAT_PORT}"
check "a stale listen address warns" \
  "$(check_config_drift 127.0.0.1 2>&1 >/dev/null | grep -c 'listen = "10.0.0.1')" 1

write_kept_config "127.0.0.1:${SERVER_PORT}" \
  "http://127.0.0.1:${ADMIN_PORT}" "127.0.0.1:16622"
check "a satellite port this run would not use warns" \
  "$(SAT_PORT=16999 check_config_drift 127.0.0.1 2>&1 >/dev/null | grep -c 'addr = "127.0.0.1:16622"')" 1

write_kept_config "127.0.0.1:${SERVER_PORT}" \
  "http://127.0.0.1:8000" "127.0.0.1:${SAT_PORT}"
check "an admin port this run would not use warns" \
  "$(ADMIN_PORT=8999 check_config_drift 127.0.0.1 2>&1 >/dev/null | grep -c 'base_url = "http://127.0.0.1:8000"')" 1

current_config

# --- host_name ----------------------------------------------------------------
#
# What `up` and `status` tell the operator to point a client at. The tailnet DNS
# name resolves to this host's Tailscale address, so naming it when the stack is
# bound somewhere else sends the client to a host nothing is listening on.

echo "# host_name"

TAILNET_IP=100.64.0.1
TAILNET_DNS=host.tailnet.ts.net
tailscale_ip() { echo "$TAILNET_IP"; }
tailscale_dns() { echo "$TAILNET_DNS"; }

# DEVSTACK_HOST is exported for the whole file and short-circuits host_name, so
# it is passed per call here rather than inherited.
named_host() { DEVSTACK_BIND="$1" DEVSTACK_HOST="${2:-}" host_name; }

check "the tailnet bind gets the tailnet name" "$(named_host "$TAILNET_IP")" "$TAILNET_DNS"
check "a loopback bind gets loopback" "$(named_host 127.0.0.1)" "127.0.0.1"
check "another address gets that address" "$(named_host 192.168.1.50)" "192.168.1.50"
# A wildcard is bound everywhere, so the tailnet name does reach it.
check "a wildcard bind keeps the tailnet name" "$(named_host 0.0.0.0)" "$TAILNET_DNS"
check "an IPv6 wildcard bind does too" "$(named_host ::)" "$TAILNET_DNS"
check "DEVSTACK_HOST wins over all of it" \
  "$(named_host 127.0.0.1 chosen.example)" "chosen.example"

# Off Tailscale entirely: there is no name to prefer.
tailscale_dns() { echo ""; }
check "no tailnet name falls back to the bind address" \
  "$(named_host "$TAILNET_IP")" "$TAILNET_IP"
tailscale_dns() { echo "$TAILNET_DNS"; }

unset -f tailscale_ip tailscale_dns
unset -f named_host

# --- publish_flags ------------------------------------------------------------
#
# What the container publishes, and to whom. The Satellite port on the tailnet
# would be an unauthenticated surface endpoint for every device on it.

echo "# publish_flags"

# The Satellite port is never published beyond loopback, whatever the bind —
# it would be an unauthenticated surface endpoint for everyone who can reach it.
for bind in 100.64.0.1 0.0.0.0 "::" fd7a::1; do
  check "the satellite port stays on loopback for a $bind bind" \
    "$(publish_flags "$bind" | grep -c "127.0.0.1:${SAT_PORT}:16622")" 1
  check "and is published nowhere else for a $bind bind" \
    "$(publish_flags "$bind" | grep -o -- "-p [^ ]*:${SAT_PORT}:16622" | wc -l | tr -d ' ')" 1
done

check "the admin port is published on the bind address" \
  "$(publish_flags 100.64.0.1 | grep -c "100.64.0.1:${ADMIN_PORT}:8000")" 1
check "loopback is published too" \
  "$(publish_flags 100.64.0.1 | grep -c "127.0.0.1:${ADMIN_PORT}:8000")" 1
check "a loopback bind is not published twice" \
  "$(publish_flags 127.0.0.1 | grep -o "127.0.0.1:${ADMIN_PORT}:8000" | wc -l | tr -d ' ')" 1

# A wildcard admin publish subsumes the loopback one on that port and cannot
# sit beside it, so it replaces it — that port only.
check "a wildcard bind does not also publish admin on loopback" \
  "$(publish_flags 0.0.0.0 | grep -c "127.0.0.1:${ADMIN_PORT}:8000")" 0
check "a wildcard bind publishes admin on the wildcard" \
  "$(publish_flags 0.0.0.0 | grep -c "0.0.0.0:${ADMIN_PORT}:8000")" 1
check "a wildcard bind publishes exactly two ports" \
  "$(publish_flags 0.0.0.0 | grep -o -- '-p' | wc -l | tr -d ' ')" 2

# Every address in a -p flag has to be one the engine will accept.
check "an IPv6 bind is bracketed" \
  "$(publish_flags fd7a::1 | grep -c '\[fd7a::1\]')" 1
check "an IPv6 wildcard is bracketed too" \
  "$(publish_flags :: | grep -c '\[::\]')" 1
# publish_flags names "[::]" as a value it accepts, so bracketing must not
# double up: the engine rejects "[[::]]".
check "an already-bracketed address is left alone" "$(publish_host '[::]')" "[::]"
check "and so is a bracketed literal" "$(publish_host '[fd7a::1]')" "[fd7a::1]"
check "a bracketed bind produces no double brackets" \
  "$(publish_flags '[::]' | grep -c '\[\[')" 0
check "no unbracketed IPv6 address reaches -p" \
  "$(publish_flags :: | grep -c -- '-p :::')" 0

# --- check_config_version -----------------------------------------------------
#
# Companion migrates its config directory in place, so running an older tag
# against a directory a newer one has written is worth saying out loud.

echo "# check_config_version"

VERSION_MARKER="$STATE_DIR/companion-version"

version_warning() {
  printf '%s' "$1" > "$VERSION_MARKER"
  { VERSION="$2" check_config_version >/dev/null; } 2>&1
}

check "the same tag is silent" "$(version_warning v3.4.1 v3.4.1 | wc -l | tr -d ' ')" 0
check "a change of tag is named" \
  "$(version_warning v3.4.1 v5.0.3 | grep -c 'last used by Companion v3.4.1')" 1
check "a downgrade says so" \
  "$(version_warning v5.0.3 v3.4.1 | grep -c 'that is a downgrade')" 1
check "an upgrade does not" \
  "$(version_warning v3.4.1 v5.0.3 | grep -c 'that is a downgrade')" 0
# Compared as versions: as strings, "v9.0.0" sorts after "v10.0.0".
check "v9 to v10 is an upgrade, not a downgrade" \
  "$(version_warning v9.0.0 v10.0.0 | grep -c 'that is a downgrade')" 0
check "v10 to v9 is a downgrade" \
  "$(version_warning v10.0.0 v9.0.0 | grep -c 'that is a downgrade')" 1

# The warning is only possible because the previous run recorded its tag. The
# helper above writes the marker itself, which hides whether the function does.
rm -f "$VERSION_MARKER"
VERSION=v3.4.1 check_config_version >/dev/null 2>&1
check "the tag in use is recorded" "$(cat "$VERSION_MARKER" 2>/dev/null)" v3.4.1
check "and a later run reads it back" \
  "$({ VERSION=v5.0.3 check_config_version >/dev/null; } 2>&1 | grep -c 'last used by Companion v3.4.1')" 1
rm -f "$VERSION_MARKER"

# --- the wiring ---------------------------------------------------------------
#
# Every check above exercises a function directly. That leaves the calls
# themselves unpinned: a refactor could drop them from `up` and the whole suite
# would stay green while the behaviour disappeared.

echo "# up calls the checks it is documented to make"

write_kept_config "10.0.0.1:${SERVER_PORT}" \
  "http://127.0.0.1:${ADMIN_PORT}" "127.0.0.1:${SAT_PORT}"
check "write_config drift-checks a config it keeps" \
  "$(write_config 127.0.0.1 2>&1 >/dev/null | grep -c 'listen = "10.0.0.1')" 1

restart_without_building() {
  (
    build_server() { :; }
    launch_server() { :; }
    wait_for_surface() { return 0; }
    cmd_status() { :; }
    cmd_restart
  )
}
# The kept config names 127.0.0.1, so a drift check told to compare against a
# hardcoded 127.0.0.1 would find nothing. It has to use this run's bind.
write_kept_config "127.0.0.1:${SERVER_PORT}" \
  "http://127.0.0.1:${ADMIN_PORT}" "127.0.0.1:${SAT_PORT}"
check "restart drift-checks against this run's bind, before it builds" \
  "$(DEVSTACK_BIND=10.0.0.5 restart_without_building 2>&1 >/dev/null | grep -c 'listen = "127.0.0.1')" 1
check "and is quiet when the config already matches" \
  "$(DEVSTACK_BIND=127.0.0.1 restart_without_building 2>&1 >/dev/null | grep -c 'listen = ')" 0

write_kept_config "10.0.0.1:${SERVER_PORT}" \
  "http://127.0.0.1:${ADMIN_PORT}" "127.0.0.1:${SAT_PORT}"

current_config

# start_companion reuses a running container, and has to say when what it
# publishes is not what this run would ask for.
export CONTAINER_ENGINE="$FAKE_ENGINE"

# Loopback-only, which is what a container created under DEVSTACK_BIND=127.0.0.1
# publishes. Asking about 127.0.0.1 matches it; asking about the address this
# run actually wants does not — so a check that ignored its argument would pass
# the first and be caught by the second.
LOOPBACK_PORTS="127.0.0.1:${ADMIN_PORT}->8000/tcp, 127.0.0.1:${SAT_PORT}->16622/tcp"
check "a loopback-only container satisfies a loopback bind" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$LOOPBACK_PORTS" say companion_publishes 127.0.0.1)" yes
check "but not a tailnet bind" \
  "$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$LOOPBACK_PORTS" say companion_publishes 100.64.0.1)" no

START_OUT="$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$LOOPBACK_PORTS" start_companion 100.64.0.1 2>&1)"
check "start_companion reuses a container whose image matches" \
  "$(printf '%s' "$START_OUT" | grep -c 'already running')" 1
check "and warns when its publish list is not this run's" \
  "$(printf '%s' "$START_OUT" | grep -c "not what this run would ask for")" 1

# The same container, asked about the bind it was created for: no warning.
QUIET_OUT="$(FAKE_IMAGE="$IMAGE" FAKE_PORTS="$LOOPBACK_PORTS" start_companion 127.0.0.1 2>&1)"
check "and stays quiet when it is" \
  "$(printf '%s' "$QUIET_OUT" | grep -c "not what this run would ask for")" 0

# start_companion has to record the tag it is starting, or the next run has
# nothing to compare against.
rm -f "$VERSION_MARKER"
(
  companion_ready() { return 0; }
  FAKE_IMAGE="" start_companion 127.0.0.1
) >/dev/null 2>&1
check "starting a container records its Companion tag" \
  "$(cat "$VERSION_MARKER" 2>/dev/null)" "$VERSION"
rm -f "$VERSION_MARKER"

unset CONTAINER_ENGINE

# A wildcard bind puts two unauthenticated services on every interface, so `up`
# says so rather than leaving it to the reader of the knob table.
check "a wildcard bind is called out" \
  "$(DEVSTACK_BIND=0.0.0.0 warn_wildcard_bind 2>&1 >/dev/null | grep -c 'every interface')" 1
check "an ordinary bind is not" \
  "$(DEVSTACK_BIND=100.64.0.1 warn_wildcard_bind 2>&1 >/dev/null | wc -l | tr -d ' ')" 0

up_without_starting() {
  (
    start_companion() { :; }
    start_server() { :; }
    wait_for_surface() { return 0; }
    cmd_status() { :; }
    cmd_up
  )
}
check "and up is what says it" \
  "$(DEVSTACK_BIND=0.0.0.0 up_without_starting 2>&1 >/dev/null | grep -c 'every interface')" 1

# up creates the state directory; the credentials an imported production export
# carries are only protected by the mode it gets. Loosened first, or an earlier
# command in this file would have already tightened it and the check could not
# tell the difference.
chmod 755 "$STATE_DIR"
DEVSTACK_BIND=127.0.0.1 up_without_starting >/dev/null 2>&1
check "up leaves the state directory unreadable by others" \
  "$(stat -c %a "$STATE_DIR")" 700

# down is the destructive command, and stopping the server is the half of it
# that the container engine cannot do.
cp "$BASH_BIN" "$SERVER_BIN"
spawn_server_like 'sleep 30; true'
DOWNED="$SPAWNED"
echo "$DOWNED" > "$SERVER_PID"
CONTAINER_ENGINE=/bin/true cmd_down >/dev/null 2>&1
check "down stops the server, not only the container" "$(dead "$DOWNED")" yes
wait "$DOWNED" 2>/dev/null
rm -f "$SERVER_PID"

current_config

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
