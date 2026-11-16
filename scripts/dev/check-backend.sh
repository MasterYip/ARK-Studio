#!/usr/bin/env bash
# Backend gate (deploy gate): the Go server's checks.
#
#   scripts/dev/check-backend.sh            # gofmt, vet, go test, docs-check, build,
#                                           # contract harness against the fresh binary,
#                                           # JS suite
#   scripts/dev/check-backend.sh --quick    # same (kept for compatibility with later
#                                           # versions that add a slower golden replay)
#   scripts/dev/check-backend.sh --legacy   # also the differential replay against the
#                                           # frozen Python server (several minutes)
#
# Needs: go (1.27+), a Python 3.10+ interpreter as test runner (ARK_PYTHON,
# else python3/python; the harness builds its fixture with the repository's
# Python package, but the server under test is ark-server), node (JS suite).
# Exits non-zero when any step fails; prints a summary.
set -u
root="$(cd "$(dirname "$0")/../.." && pwd)"
quick=0
legacy=0
for arg in "$@"; do
  case "$arg" in
    --quick) quick=1 ;;
    --legacy) legacy=1 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

py="${ARK_PYTHON:-}"
if [ -z "$py" ]; then
  if command -v python3 >/dev/null 2>&1 && python3 -c 'import sys; sys.exit(sys.version_info < (3, 10))' 2>/dev/null; then
    py=python3
  else
    py=python
  fi
fi
exe=""
case "$(go env GOOS 2>/dev/null)" in windows) exe=".exe" ;; esac
bin="$root/go/ark-server$exe"
if command -v cygpath >/dev/null 2>&1; then bin_arg="$(cygpath -m "$bin")"; else bin_arg="$bin"; fi
export TEMP="${TEMP:-${TMPDIR:-/tmp}}" TMP="${TMP:-${TMPDIR:-/tmp}}"

names=()
results=()
failed=0
step() {
  local name="$1"; shift
  local start=$SECONDS
  echo "==> $name"
  if "$@"; then
    results+=("ok    $name ($((SECONDS - start))s)")
  else
    results+=("FAIL  $name ($((SECONDS - start))s)")
    failed=1
  fi
  names+=("$name")
}

gofmt_clean() {
  local out
  out="$(cd "$root/go" && gofmt -l ./cmd ./internal)"
  [ -z "$out" ] || { echo "gofmt needed:"; echo "$out"; return 1; }
}

step "gofmt" gofmt_clean
step "go vet" bash -c "cd '$root/go' && go vet ./..."
step "agent docs (generate, docs-check)" bash -c "cd '$root/go' && go generate ./internal/agentdocs && go run ./cmd/ark-server docs-check --docs ../docs"
step "go test" bash -c "cd '$root/go' && go test ./..."
step "build ark-server" bash -c "cd '$root/go' && go build -o '$bin' ./cmd/ark-server"
step "contract tools (go_only)" bash -c "cd '$root' && '$py' -B -m unittest tests.python.contract.test_go_only"
step "contract harness (ark-server)" bash -c "cd '$root' && ARK_CONTRACT_SERVER='$bin_arg' '$py' -B tests/python/contract/harness.py"
: "$quick"  # no golden replay on this branch yet
step "JS suite" bash -c "cd '$root' && node --experimental-vm-modules --test tests/js/"
if [ "$legacy" = 1 ]; then
  step "legacy: differential replay vs Python" bash -c "cd '$root' && '$py' -B tests/python/contract/diffreplay.py --go '$bin_arg'"
fi

echo
echo "Backend gate summary:"
for line in "${results[@]}"; do echo "  $line"; done
if [ "$failed" = 0 ]; then echo "PASS"; else echo "FAIL"; fi
exit "$failed"
