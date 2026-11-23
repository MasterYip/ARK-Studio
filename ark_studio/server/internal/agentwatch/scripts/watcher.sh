#!/bin/sh
# ARK watcher (GET /api/agent/v1/watcher.sh; downloading it needs no token).
# Prefer `ark-server agent watch --until-update`, which does the same.  To
# use this script, save it once with your file-writing tool as ark-watch.sh
# and run the saved file (sh ark-watch.sh); do not pipe it into a shell.
# Waits until a person replies in ARK on your units (comments, replies, Send
# back, decisions, ...) or changes the agent rules, prints what happened and
# exits.  Handle it, then start the watcher again.
#   env: ARK_URL, ARK_PROJECT; the token is read from ARK_TOKEN_FILE (default
#        ~/.ark/tokens/<ARK_PROJECT>, readable only by you) or ARK_TOKEN and
#        handed to curl on stdin, never on a command line.
#   optional: ARK_AGENT_LABEL (your name in messages), ARK_HOST (this
#        device's id), ARK_WATCH_STATE (cursor file, default .ark-watch-state),
#        ARK_WATCH_MAX (seconds to wait, default 6900; 0 = check once),
#        ARK_OWNER_PID (the agent process this watcher belongs to; default:
#        the nearest Codex or Claude Code process above the watcher).
#   Codex only: ARK_WAKE=codex ARK_CODEX_THREAD="$CODEX_THREAD_ID".  The
#        watcher then checks that it runs under that Codex session, detaches,
#        keeps watching and wakes the session with `codex queue`; when it
#        cannot, it says why and exits 2.
# The watcher stops when the agent that started it is gone, and does not
# start in a session ARK launched and wakes itself (ARK_SESSION_ID with
# ARK_WAKE=ark).
if [ -n "$ARK_SESSION_ID" ] && [ "$ARK_WAKE" = ark ]; then
  echo "ARK: ARK launched this session ($ARK_SESSION_ID) and wakes it itself when people reply; no watcher needed."
  exit 0
fi
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
# The agent's process: ARK_OWNER_PID when it is Codex or Claude Code, or a
# live process that is not above this watcher.  Otherwise (gone, the shell
# that ran this command, or not set) the nearest codex or claude process
# above the watcher, else its nearest node process (a CLI wrapper).  Only
# parent ids and process names are read.
# agent_kind NAME: claude, codex, node or nothing.
agent_kind() {
  n=${1##*/}; n=${n%.exe}
  case "$n" in codex|codex-linux-san*) echo codex ;; claude) echo claude ;; node|bun) echo node ;; esac
}
proc_name() {
  if [ -r "/proc/$1/comm" ]; then cat "/proc/$1/comm" 2>/dev/null
  else ps -o comm= -p "$1" 2>/dev/null; fi
}
parent_of() {
  if [ -r "/proc/$1/stat" ]; then sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f2
  else ps -o ppid= -p "$1" 2>/dev/null | tr -d ' '; fi
}
owner=""; owner_kind=""
if [ -n "$ARK_WATCH_DETACHED_OWNER" ]; then
  owner=$ARK_WATCH_DETACHED_OWNER
else
  chain=""; p=$PPID; i=0 # this watcher's ancestors, nearest first
  while [ -n "$p" ] && [ "$p" -ge 1 ] 2>/dev/null && [ $i -lt 64 ]; do
    case " $chain " in *" $p "*) break ;; esac
    chain="$chain $p"
    [ "$p" = 1 ] && break
    p=$(parent_of "$p"); i=$((i + 1))
  done
  given=$ARK_OWNER_PID
  if [ -n "$given" ] && kill -0 "$given" 2>/dev/null; then
    case "$(agent_kind "$(proc_name "$given")")" in
      codex|claude) owner=$given ;;
      *) case " $chain " in *" $given "*) ;; *) owner=$given ;; esac ;;
    esac
  fi
  if [ -z "$owner" ]; then
    found=""; wrapper=""
    for p in $chain; do
      case "$(agent_kind "$(proc_name "$p")")" in
        codex|claude) found=$p; break ;;
        node) [ -z "$wrapper" ] && wrapper=$p ;;
      esac
    done
    found=${found:-$wrapper}
    if [ -n "$found" ]; then
      [ -n "$given" ] && echo "ARK: pid $given is gone or not your agent; the watcher belongs to $(proc_name "$found") pid $found, the nearest agent above it."
      owner=$found
    elif [ -n "$given" ] && kill -0 "$given" 2>/dev/null; then owner=$given
    elif [ -n "$given" ]; then echo "ARK: no process $given (ARK_OWNER_PID)"; exit 2
    elif [ "${PPID:-1}" -gt 1 ]; then owner=$PPID
    fi
  fi
fi
[ -n "$owner" ] && owner_kind=$(agent_kind "$(proc_name "$owner")")
owner_alive() {
  [ -z "$owner" ] && return 0
  kill -0 "$owner" 2>/dev/null || return 1
  [ -z "$owner_kind" ] || [ "$(agent_kind "$(proc_name "$owner")")" = "$owner_kind" ]
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
  elif [ -z "$owner" ]; then why="owner process not found"
  elif [ "$owner_kind" != codex ]; then why="the owner process (pid $owner) is not Codex"
  elif [ -z "$ARK_CODEX_THREAD" ] || [ "$ARK_CODEX_THREAD" != "$CODEX_THREAD_ID" ]; then why="ARK_CODEX_THREAD is not this Codex session's thread"
  fi
  if [ -z "$why" ]; then
    for c in "${CODEX_MANAGED_PACKAGE_ROOT:+$CODEX_MANAGED_PACKAGE_ROOT/bin/codex.js}" "$(command -v codex 2>/dev/null)"; do
      if [ -n "$c" ] && [ -x "$c" ] && "$c" queue --help >/dev/null 2>&1; then codex_bin=$c; break; fi
    done
    [ -z "$codex_bin" ] && why="no codex with \`queue\` found"
  fi
  if [ -n "$why" ]; then
    # Say it and fail: a watcher that cannot wake Codex is no use.
    case "$why" in
      *Claude*) hint="Claude Code is woken when the watcher exits: drop ARK_WAKE" ;;
      *thread*) hint='set ARK_CODEX_THREAD="$CODEX_THREAD_ID" from your own session' ;;
      *queue*) hint="update Codex" ;;
      *) hint="set ARK_OWNER_PID=<codex pid>" ;;
    esac
    echo "ARK: cannot wake codex: $why; $hint"
    exit 2
  fi
  codex_thread=$ARK_CODEX_THREAD
fi
if [ -n "$codex_thread" ] && [ -z "$ARK_WATCH_DETACHED_OWNER" ]; then
  # Codex ends a command's process group when the command returns: start
  # again in a session of its own, tied to this Codex process.
  self=$0
  case "$self" in /*) ;; *) self=$PWD/$self ;; esac
  [ -f "$self" ] || { echo "ARK: save the watcher as ark-watch.sh and run that file (sh ark-watch.sh)."; exit 2; }
  export ARK_WATCH_DETACHED_OWNER=$owner ARK_URL ARK_PROJECT ARK_WAKE ARK_CODEX_THREAD
  [ -n "$tok_env" ] && export ARK_TOKEN="$tok" # the environment, not the arguments
  if command -v setsid >/dev/null 2>&1; then
    setsid -f sh "$self" </dev/null >/dev/null 2>&1
  else
    perl -MPOSIX -e 'POSIX::setsid(); exec @ARGV' sh "$self" </dev/null >/dev/null 2>&1 &
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
    head="ARK UPDATE for $label, ARK project $project_name: ${count:-0} new update(s)${changed:+; the agent rules changed to $rv (GET /api/agent/v1/rules)}."
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
