#!/bin/sh
# ARK watcher (GET /api/agent/v1/watcher.sh; downloading it needs no token).
# Waits until a person replies in ARK on your units (comments, replies, Send
# back, decisions, ...) or changes the agent rules, prints what happened and
# exits.  Handle it, then start the watcher again.
#   env: ARK_URL, ARK_PROJECT; the token is read from ARK_TOKEN_FILE (default
#        ~/.ark/tokens/<ARK_PROJECT>, readable only by you) or ARK_TOKEN and
#        handed to curl on stdin, never on a command line.
#   optional: ARK_AGENT_LABEL (your name in messages), ARK_HOST (this
#        device's id), ARK_WATCH_STATE (cursor file, default .ark-watch-state),
#        ARK_WATCH_MAX (seconds to wait, default 6900; 0 = check once),
#        ARK_OWNER_PID (the agent process this watcher belongs to).
#   Codex only: ARK_WAKE=codex ARK_CODEX_THREAD="$CODEX_THREAD_ID".  The
#        watcher then checks that it runs under that Codex session, detaches,
#        keeps watching and wakes the session with `codex queue`.
# The watcher stops when the agent that started it is gone.
: "${ARK_URL:?set ARK_URL}" "${ARK_PROJECT:?set ARK_PROJECT}"
ARK_URL=${ARK_URL%/}

# ---------------------------------------------------------------- the token
tf=${ARK_TOKEN_FILE:-$HOME/.ark/tokens/$ARK_PROJECT}
tok=""
if [ -f "$tf" ]; then
  chmod 600 "$tf" 2>/dev/null
  IFS= read -r tok < "$tf" || [ -n "$tok" ]
  tok=$(printf '%s' "$tok" | tr -d '\r\n ')
fi
tok_env=""
[ -z "$tok" ] && tok=$ARK_TOKEN && tok_env=1
unset ARK_TOKEN
if [ -z "$tok" ]; then
  echo "ARK: no token: write it to $tf (only you may read it) or set ARK_TOKEN in this shell's environment."
  exit 2
fi
# curl reads the headers from stdin (-K -): printf is a shell builtin, so
# the token never appears in a process's arguments.
ark_get() { # path, max seconds; prints body, newline, HTTP status
  printf 'header = "Authorization: Bearer %s"\nheader = "X-ARK-Project: %s"\n' "$tok" "$ARK_PROJECT" |
    curl -sS -K - -m "$2" -w '\n%{http_code}' ${ARK_HOST:+-H "X-ARK-Host: $ARK_HOST"} "$ARK_URL$1" ||
    printf '\n000'
}

# ---------------------------------------------------------------- the owner
# agent_kind PID: claude, codex or nothing.
agent_kind() {
  if [ -r "/proc/$1/comm" ]; then
    comm=$(cat "/proc/$1/comm" 2>/dev/null)
    args=$(tr '\0' ' ' < "/proc/$1/cmdline" 2>/dev/null)
  else
    comm=$(ps -o comm= -p "$1" 2>/dev/null)
    args=$(ps -o args= -p "$1" 2>/dev/null)
  fi
  comm=${comm##*/}
  set -f; set -- $args; set +f
  a0=${1##*/}; a1=${2##*/}
  case "$comm" in claude|claude.exe) echo claude; return ;; codex|codex.exe|codex-linux-san*) echo codex; return ;; esac
  case "$a0" in claude|claude.exe) echo claude; return ;; codex|codex.exe|codex-linux-sandbox) echo codex; return ;; esac
  case "$comm" in node|node.exe|bun)
    case "$a1 $args" in claude\ *|*/claude-code/*|*/claude-agent-sdk/*) echo claude; return ;;
      codex\ *|codex.js\ *|*/@openai/codex/*) echo codex; return ;; esac ;;
  esac
}
parent_of() {
  if [ -r "/proc/$1/stat" ]; then sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f2
  else ps -o ppid= -p "$1" 2>/dev/null | tr -d ' '; fi
}
owner=""; owner_kind=""
if [ -n "$ARK_WATCH_DETACHED_OWNER" ]; then
  owner=$ARK_WATCH_DETACHED_OWNER
  owner_kind=$(agent_kind "$owner")
elif [ -n "$ARK_OWNER_PID" ]; then
  owner=$ARK_OWNER_PID
  owner_kind=$(agent_kind "$owner")
else # the nearest agent among this watcher's ancestors
  p=$PPID; i=0
  while [ -n "$p" ] && [ "$p" -gt 1 ] 2>/dev/null && [ $i -lt 40 ]; do
    k=$(agent_kind "$p")
    if [ -n "$k" ]; then owner=$p; owner_kind=$k; break; fi
    p=$(parent_of "$p"); i=$((i + 1))
  done
  # In the Codex sandbox (its own PID namespace) the sandbox is pid 1.
  if [ -z "$owner" ] && [ "$p" = 1 ] && [ "$(agent_kind 1)" = codex ]; then owner=1; owner_kind=codex; fi
  [ -z "$owner" ] && [ "${PPID:-1}" -gt 1 ] && owner=$PPID
fi
owner_alive() {
  [ -z "$owner" ] && return 0
  kill -0 "$owner" 2>/dev/null || return 1
  [ -z "$owner_kind" ] || [ "$(agent_kind "$owner")" = "$owner_kind" ]
}

# ---------------------------------------------------------------- Codex wake-up
# Only when this Codex session asked for it (ARK_WAKE=codex with its own
# thread id), the nearest agent above the watcher is Codex, and the thread
# id Codex gave this command matches.  An inherited CODEX_THREAD_ID alone
# never wakes anything: it can belong to an unrelated session.
codex_bin=""; codex_thread=""
if [ "$ARK_WAKE" = codex ]; then
  why=""
  if [ -n "$CLAUDECODE" ] || [ -n "$CLAUDE_CODE_ENTRYPOINT" ]; then why="this is a Claude Code session"
  elif [ "$owner_kind" != codex ]; then why="no Codex process above the watcher"
  elif [ -z "$ARK_CODEX_THREAD" ] || [ "$ARK_CODEX_THREAD" != "$CODEX_THREAD_ID" ]; then why="ARK_CODEX_THREAD is not this Codex session's thread"
  fi
  if [ -z "$why" ]; then
    for c in "${CODEX_MANAGED_PACKAGE_ROOT:+$CODEX_MANAGED_PACKAGE_ROOT/bin/codex.js}" "$(command -v codex 2>/dev/null)"; do
      if [ -n "$c" ] && [ -x "$c" ] && "$c" queue --help >/dev/null 2>&1; then codex_bin=$c; break; fi
    done
    [ -z "$codex_bin" ] && why="no codex with \`queue\` found"
  fi
  if [ -n "$why" ]; then
    echo "ARK: not waking through codex queue ($why); the watcher exits on news instead."
  else
    codex_thread=$ARK_CODEX_THREAD
  fi
fi
if [ -n "$codex_thread" ] && [ -z "$ARK_WATCH_DETACHED_OWNER" ]; then
  # Codex ends a command's process group when the command returns: start
  # again in a session of its own, tied to this Codex process.
  script=$(curl -fsS -m 30 "$ARK_URL/api/agent/v1/watcher.sh") || { echo "ARK: could not download the watcher again"; exit 1; }
  export ARK_WATCH_DETACHED_OWNER=$owner ARK_URL ARK_PROJECT ARK_WAKE ARK_CODEX_THREAD
  [ -n "$tok_env" ] && export ARK_TOKEN="$tok" # the environment, not the arguments
  if command -v setsid >/dev/null 2>&1; then
    setsid -f sh -c "$script" </dev/null >/dev/null 2>&1
  else
    perl -MPOSIX -e 'POSIX::setsid(); exec @ARGV' sh -c "$script" </dev/null >/dev/null 2>&1 &
  fi
  echo "ARK: the watcher runs in the background and wakes this Codex session through codex queue."
  exit 0
fi

# ---------------------------------------------------------------- the feed
project_name=$ARK_PROJECT
ctx=$(ark_get /api/agent/v1/context 20 | sed '$d')
n=$(printf '%s' "$ctx" | sed -n 's/^{"ok": true, "project": {[^}]*"name": "\([^"]*\)".*/\1/p' | head -1)
[ -n "$n" ] && project_name="$n ($ARK_PROJECT)"
label=${ARK_AGENT_LABEL:-${codex_thread:+Codex thread $codex_thread}}
label=${label:-the agent that started this watcher}
state=${ARK_WATCH_STATE:-.ark-watch-state}
if [ -n "$codex_thread" ] && [ -z "$ARK_WATCH_MAX" ]; then max=31536000; else max=${ARK_WATCH_MAX:-6900}; fi
cursor=$(sed -n 1p "$state" 2>/dev/null)
rules=$(sed -n 2p "$state" 2>/dev/null)
start=$(date +%s)
while :; do
  if ! owner_alive; then
    echo "ARK: the agent that started this watcher ($owner_kind pid $owner) is gone; the watcher stops."
    exit 0
  fi
  left=$((max - ($(date +%s) - start)))
  wait=50
  [ "$left" -lt "$wait" ] && wait=$left
  [ "$wait" -lt 0 ] && wait=0
  q="mine=1&wait=$wait"
  [ -n "$cursor" ] && q="$q&since=$cursor"
  resp=$(ark_get "/api/agent/v1/updates?$q" $((wait + 20)))
  code=$(printf '%s' "$resp" | tail -n 1)
  body=$(printf '%s' "$resp" | sed '$d')
  case "$code" in
    200) ;;
    401|403) echo "ARK: the token was refused (HTTP $code); the watcher stops."; exit 3 ;;
    *) [ "$max" -eq 0 ] && { echo "ARK: the watcher could not reach ARK (HTTP $code)"; exit 1; }
       sleep 30
       continue ;;
  esac
  new=$(printf '%s' "$body" | sed -n 's/^{"ok": true, "cursor": "\([^"]*\)".*/\1/p')
  count=$(printf '%s' "$body" | sed -n 's/.*"count": \([0-9][0-9]*\), "more".*/\1/p')
  rv=$(printf '%s' "$body" | sed -n 's/.*"rules_version": "\(r-[0-9a-f]*\)".*/\1/p')
  [ -n "$new" ] && cursor=$new
  changed=""
  [ -n "$rules" ] && [ -n "$rv" ] && [ "$rv" != "$rules" ] && changed=1
  [ -n "$rv" ] && rules=$rv
  printf '%s\n%s\n' "$cursor" "$rules" > "$state"
  if [ "${count:-0}" -gt 0 ] || [ -n "$changed" ]; then
    head="ARK UPDATE for $label, ARK project $project_name at $ARK_URL: ${count:-0} new update(s)${changed:+; the agent rules changed to $rv (GET /api/agent/v1/rules)}."
    echo "$head Act on them (the items below, or GET /api/agent/v1/updates), then start this watcher again."
    printf '%s\n' "$body"
    if [ -n "$codex_thread" ]; then
      # escaped quotes become ' so that a plain pattern finds whole summaries
      items=$(printf '%s' "$body" | sed 's/\\"/'"'"'/g' | grep -o '"summary": "[^"]*"' | sed 's/^"summary": "/- /; s/"$//' | head -10)
      "$codex_bin" queue --thread "$codex_thread" --message "$head
$items
This message comes from the ARK watcher you started for this project. If you are not working on ARK project $project_name, ignore it. Otherwise act on the updates under the core rules (GET /api/agent/v1/units/<ID>/comments for the full comments, reply with reply_to, resolve), then start the ARK watcher again." >/dev/null 2>&1 ||
        echo "ARK: codex queue failed; read GET /api/agent/v1/updates yourself."
    fi
    exit 0
  fi
  if [ $(($(date +%s) - start)) -ge "$max" ]; then
    echo "ARK: no updates in ${max}s; start the watcher again."
    exit 0
  fi
done
