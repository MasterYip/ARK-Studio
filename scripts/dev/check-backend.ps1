<#
Backend gate (deploy gate): the Go server's checks.

  scripts/dev/check-backend.ps1            # gofmt, vet, go test, docs-check, build,
                                           # contract harness against the fresh binary,
                                           # JS suite
  scripts/dev/check-backend.ps1 -Quick     # same (kept for compatibility with later
                                           # versions that add a slower golden replay)
  scripts/dev/check-backend.ps1 -Legacy    # also the differential replay against the
                                           # frozen Python server (several minutes)

Needs: go (1.27+), a Python 3.10+ interpreter as test runner ($env:ARK_PYTHON,
else python; the harness builds its fixture with the repository's Python
package, but the server under test is ark-server), node (JS suite).
Exits non-zero when any step fails; prints a summary.
#>
param([switch]$Quick, [switch]$Legacy)

$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$go = Join-Path $root 'go'
$py = $env:ARK_PYTHON
if (-not $py) { $py = 'python' }
$bin = Join-Path $go 'ark-server.exe'
$results = New-Object System.Collections.Generic.List[string]
$script:failed = $false

function Step([string]$name, [scriptblock]$body) {
    Write-Host "==> $name"
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $global:LASTEXITCODE = 0
    $ok = $true
    try { & $body } catch { Write-Host $_; $ok = $false }
    if ($LASTEXITCODE -ne 0) { $ok = $false }
    $secs = [int]$watch.Elapsed.TotalSeconds
    if ($ok) { $results.Add("ok    $name (${secs}s)") } else { $results.Add("FAIL  $name (${secs}s)"); $script:failed = $true }
}

Push-Location $go
try {
    Step 'gofmt' {
        $out = gofmt -l ./cmd ./internal
        if ($out) { Write-Host "gofmt needed:"; $out | ForEach-Object { Write-Host $_ }; $global:LASTEXITCODE = 1 }
    }
    Step 'go vet' { go vet ./... }
    Step 'agent docs (generate, docs-check)' {
        go generate ./internal/agentdocs
        if ($LASTEXITCODE -eq 0) { go run ./cmd/ark-server docs-check --docs ../docs }
    }
    Step 'go test' { go test ./... }
    Step 'build ark-server' { go build -o $bin ./cmd/ark-server }
} finally { Pop-Location }

Push-Location $root
try {
    Step 'contract tools (go_only)' { & $py -B -m unittest tests.python.contract.test_go_only }
    Step 'contract harness (ark-server)' {
        $env:ARK_CONTRACT_SERVER = $bin
        try { & $py -B tests/python/contract/harness.py } finally { Remove-Item Env:ARK_CONTRACT_SERVER }
    }
    $null = $Quick  # no golden replay on this branch yet
    Step 'JS suite' { node --experimental-vm-modules --test tests/js/ }
    if ($Legacy) {
        Step 'legacy: differential replay vs Python' { & $py -B tests/python/contract/diffreplay.py --go $bin }
    }
} finally { Pop-Location }

Write-Host ''
Write-Host 'Backend gate summary:'
$results | ForEach-Object { Write-Host "  $_" }
if ($script:failed) { Write-Host 'FAIL'; exit 1 }
Write-Host 'PASS'
exit 0
