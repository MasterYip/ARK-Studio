#!/usr/bin/env sh
# POSIX counterpart of scripts/dev/start.ps1 (Ubuntu/Linux and macOS).
# Usage: ./scripts/dev/start.sh
# Env:   PORT=8890 ROOT=/path/to/projects NO_BROWSER=1 PYTHON=python3
set -eu

PORT="${PORT:-8890}"
ROOT="${ROOT:-}"
NO_BROWSER="${NO_BROWSER:-0}"
PYTHON="${PYTHON:-}"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SCRIPT_DIR/../.."

if [ -z "$PYTHON" ]; then
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1; then
      PYTHON="$candidate"
      break
    fi
  done
fi
if [ -z "$PYTHON" ]; then
  echo "ARK Studio needs Python 3.10 or newer; python3 was not found on PATH." >&2
  exit 1
fi

set --
if [ -n "$ROOT" ]; then
  set -- --root "$ROOT"
fi

if [ "$NO_BROWSER" != "1" ]; then
  # Let the server bind first, then use the same cross-platform browser helper
  # as the rest of ARK. Failures here must not stop the server.
  (
    sleep 2
    "$PYTHON" -c 'import sys; from arkstudio.platform_support import default_browser_open; default_browser_open(sys.argv[1])' \
      "http://127.0.0.1:$PORT/" >/dev/null 2>&1 || true
  ) &
fi

exec "$PYTHON" -m arkstudio serve --port "$PORT" "$@"
