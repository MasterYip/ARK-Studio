<#
Backend gate (deploy gate): the Go server's own checks, no Python server.

  scripts/dev/check-backend.ps1            # everything: gofmt, vet, go test ./..., docs-check,
                                           # build, contract harness + golden replay against
                                           # the fresh binary, JS suite
  scripts/dev/check-backend.ps1 -Quick     # only the checks the change can break (see below);
                                           # never the golden replay
  scripts/dev/check-backend.ps1 -Quick -All   # every check except the golden replay
  scripts/dev/check-backend.ps1 -Quick -Base <rev>   # also compare against <rev> (a revision
                                           # that passed the gate, e.g. the deployed one)
  scripts/dev/check-backend.ps1 -Legacy    # also the differential replay against the
                                           # frozen Python server (skipped without ark_studio/python)

-Quick compares the tree with the closest revision that already passed (a pass stamp, or
-Base) and runs:
  gofmt          changed .go files
  go vet         packages with changed .go files
  go test        packages affected by the change (changed packages, packages whose tests read
                 the changed non-Go files, and everything that imports them, tests included)
                 plus internal/store and internal/server; Go's test cache skips the rest
  docs-check     when docs/, internal/agentdocs or cmd/docsgen changed
  build          when any Go package is affected or the contract harness runs
  contract       when non-test Go code in internal/server, internal/api or cmd/ark-server
                 (handlers, routes), tests/python/contract or the contract route list changed
  JS suite       when web/, desktop/, tests/js, scripts/release or a file the JS tests read
                 changed; runs beside go test
Without a passed revision to compare with, or when this script or go.mod changed, -Quick
runs every check (as -All).

A run that passes on a clean checkout writes a pass stamp (revision, tree, script hash, Go
version) to $env:ARK_GATE_STAMPS (default
ark-gate-stamps in the Git common dir); gate_and_deploy skips the gate for a stamped revision.
-NoStamp neither reads nor writes stamps.

Needs: go (1.27+), a Python 3.10+ interpreter as test runner ($env:ARK_PYTHON,
else python; it never imports arkstudio in the Go-only steps), node (JS suite).
Exits non-zero when any step fails; prints a summary.
#>
param([switch]$Quick, [switch]$Legacy, [switch]$All, [string]$Base = '', [switch]$NoStamp)

$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$go = Join-Path $root 'ark_studio/server'
$py = $env:ARK_PYTHON
if (-not $py) { $py = 'python' }
$bin = Join-Path $go 'ark-server.exe'
$module = 'arkstudio/go'
$results = New-Object System.Collections.Generic.List[string]
$script:failed = $false
$total = [Diagnostics.Stopwatch]::StartNew()
$par = [Math]::Max(2, [int]([Environment]::ProcessorCount / 2))

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
function Skip([string]$name, [string]$why) {
    Write-Host "==> $name`: skipped ($why)"
    $results.Add("skip  $name ($why)")
}
function Get-Sha256([string]$path) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { ([BitConverter]::ToString($sha.ComputeHash([IO.File]::ReadAllBytes($path))) -replace '-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}
# go test runs with a short, fixed PATH: the test cache keys on the environment the tests read,
# and Go re-checks every executable lookup in the test log before it reuses a result (with the
# full PATH of about 80 folders that check alone took minutes for internal/server).
function Use-TestPath {
    $script:savedPath, $script:savedPathExt = $env:PATH, $env:PATHEXT
    if ($env:OS -ne 'Windows_NT') { return }
    $dirs = New-Object System.Collections.Generic.List[string]
    $add = { param($d) if ($d -and (Test-Path -LiteralPath $d) -and -not $dirs.Contains($d)) { $dirs.Add($d) } }
    & $add (Join-Path ((go env GOROOT) | Out-String).Trim() 'bin')
    foreach ($tool in 'git', 'node', $py, 'powershell', 'ssh') {
        $c = Get-Command $tool -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($c) { & $add (Split-Path -Parent $c.Source) }
    }
    foreach ($d in "$env:SystemRoot\System32", $env:SystemRoot, "$env:SystemRoot\System32\OpenSSH") { & $add $d }
    $env:PATH = $dirs -join ';'
    $env:PATHEXT = '.COM;.EXE;.BAT;.CMD'
}
function Restore-Path { $env:PATH, $env:PATHEXT = $script:savedPath, $script:savedPathExt }
function Test-Prefix([string[]]$files, [string[]]$prefixes) {
    foreach ($f in $files) { foreach ($p in $prefixes) { if ($f.StartsWith($p)) { return $true } } }
    return $false
}

# ---- revision, cleanliness, stamps ------------------------------------------------------
$head = (git -C $root rev-parse --verify -q 'HEAD^{commit}' 2>$null)
$dirtyAtStart = [bool](git -C $root status --porcelain 2>$null)
$goVersion = ((go env GOVERSION) | Out-String).Trim()
$scriptHash = Get-Sha256 $PSCommandPath
$stampDir = $env:ARK_GATE_STAMPS
if (-not $stampDir) {
    $common = (git -C $root rev-parse --path-format=absolute --git-common-dir 2>$null)
    if ($common) { $stampDir = Join-Path $common 'ark-gate-stamps' }
}
if ($NoStamp) { $stampDir = '' }
# ---- what changed since the closest passed revision -------------------------------------
# Files outside their package that Go tests read (update when a test starts reading one).
$goReads = [ordered]@{
    'docs/'                                  = @('internal/agentdocs', 'internal/api/agentdocs', 'internal/server')
    'docs/operations/gpu_servers.example.yaml' = @('internal/gpu')
    'ark_studio/web/js/starter-prompts.js'   = @('internal/agentrules')
    'ark_studio/python/'                     = @('internal/sessionctx', 'internal/sessionwatch')
    'tests/js/fixtures/'                     = @('internal/sessions')
}
$smoke = @('internal/store', 'internal/server')
# Everything when one of these changes.
$allTriggers = @('scripts/dev/check-backend.ps1', 'ark_studio/server/go.mod', 'ark_studio/server/go.sum', '.gitattributes')
$jsPrefixes = @('ark_studio/web/', 'ark_studio/desktop/', 'tests/js/', 'scripts/release/', 'ark_studio/python/arkstudio/',
                'ark_studio/server/internal/settings/graphdisplay.go', 'docs/agents/working-mode.md', 'package.json')
$docsPrefixes = @('docs/', 'ark_studio/server/internal/agentdocs/', 'ark_studio/server/cmd/docsgen/',
                  'ark_studio/server/cmd/ark-server/docs_check.go')
$contractPrefixes = @('tests/python/contract/', 'docs/development/go-port/')
$contractGoPrefixes = @('ark_studio/server/internal/server/', 'ark_studio/server/internal/api/', 'ark_studio/server/cmd/ark-server/')

$selective = $Quick -and -not $All
$why = if ($All) { '-All' } elseif (-not $Quick) { 'full gate' } else { '' }
$changed = @()
$baseRev = ''
if ($selective) {
    $cands = New-Object System.Collections.Generic.List[string]
    if ($Base) {
        $r = git -C $root rev-parse --verify -q "$Base^{commit}" 2>$null
        if ($r) { $cands.Add($r) } else { Write-Host "warning: -Base $Base is not a known revision" }
    }
    if ($stampDir -and (Test-Path -LiteralPath $stampDir)) {
        Get-ChildItem -LiteralPath $stampDir -Filter '*.json' | Sort-Object LastWriteTime -Descending | Select-Object -First 30 | ForEach-Object {
            try { $s = Get-Content -Raw -LiteralPath $_.FullName | ConvertFrom-Json } catch { return }
            if ($s.goVersion -ne $goVersion -or -not $s.lf -or $s.revision -notmatch '^[0-9a-f]{40}$') { return }
            git -C $root cat-file -e "$($s.revision)^{commit}" 2>$null
            if ($LASTEXITCODE -eq 0 -and -not $cands.Contains($s.revision)) { $cands.Add($s.revision) }
        }
    }
    $best = $null
    foreach ($c in $cands) {
        $files = @(git -C $root -c core.quotepath=false diff --name-only --no-renames $c -- 2>$null)
        if ($LASTEXITCODE -ne 0) { continue }
        if ($null -eq $best -or $files.Count -lt $best.Count) { $best = @{ Rev = $c; Files = $files; Count = $files.Count } }
    }
    if ($null -eq $best) {
        $selective = $false; $why = 'no passed revision to compare with'
    } else {
        $baseRev = $best.Rev
        $untracked = @(git -C $root -c core.quotepath=false ls-files --others --exclude-standard 2>$null)
        $changed = @(@($best.Files) + $untracked | Where-Object { $_ } | Sort-Object -Unique)
        $hit = @($changed | Where-Object { $allTriggers -contains $_ })
        if ($hit) { $selective = $false; $why = "$($hit[0]) changed" }
    }
}
$testPkgs = @(); $vetPkgs = @(); $fmtFiles = @()
$needGoTest = $true; $needVet = $true; $needDocs = $true; $needContract = $true; $needJs = $true; $needBuild = $true
if ($selective) {
    $goChanged = @($changed | Where-Object { $_.StartsWith('ark_studio/server/') -and $_.EndsWith('.go') })
    $fmtFiles = @($goChanged | Where-Object { Test-Path -LiteralPath (Join-Path $root $_) } | ForEach-Object { Join-Path $root $_ })
    # package dirs (repo-relative) and, per test binary, the packages it links
    $dirs = @{}; $testDeps = @{}
    Push-Location $go
    try {
        $lines = @(go list -e -test -f '{{.ImportPath}}|{{.Dir}}|{{range .Deps}}{{.}},{{end}}' ./...)
        if ($LASTEXITCODE -ne 0) { $selective = $false; $why = 'go list failed' }
    } finally { Pop-Location }
    $rootFwd = $root.Replace('\', '/').TrimEnd('/') + '/'
    foreach ($line in $lines) {
        $parts = $line.Split('|')
        if ($parts.Count -lt 3) { continue }
        $ip = $parts[0]
        if ($ip.EndsWith('.test')) {
            $owner = $ip.Substring(0, $ip.Length - 5)
            $testDeps[$owner] = @($parts[2].Split(',') | Where-Object { $_ } | ForEach-Object { ($_ -split ' \[')[0] })
        } elseif ($ip -notmatch ' \[' -and $parts[1]) {
            $d = $parts[1].Replace('\', '/')
            if ($d.StartsWith($rootFwd, [StringComparison]::OrdinalIgnoreCase)) { $dirs[$d.Substring($rootFwd.Length) + '/'] = $ip }
        }
    }
}
if ($selective) {
    $direct = New-Object System.Collections.Generic.HashSet[string]   # changed packages (importers rerun)
    $readers = New-Object System.Collections.Generic.HashSet[string]  # tests that read changed files
    $vetSet = New-Object System.Collections.Generic.HashSet[string]
    $sortedDirs = @($dirs.Keys | Sort-Object Length -Descending)
    foreach ($f in $changed) {
        if ($f.StartsWith('ark_studio/server/')) {
            $owner = $sortedDirs | Where-Object { $f.StartsWith($_) } | Select-Object -First 1
            if ($owner) {
                [void]$direct.Add($dirs[$owner])
                if ($f.EndsWith('.go')) { [void]$vetSet.Add($dirs[$owner]) }
            } elseif ($f.EndsWith('.go')) {
                # a removed package: its importers changed too and are caught by their own files
                Write-Host "note: $f is not in a current package"
            }
            if ($f.EndsWith('.go')) { [void]$readers.Add("$module/internal/archtest") }
        }
        foreach ($p in $goReads.Keys) {
            if ($f.StartsWith($p)) { foreach ($pkg in $goReads[$p]) { [void]$readers.Add("$module/$pkg") } }
        }
    }
    $tests = New-Object System.Collections.Generic.HashSet[string]
    foreach ($owner in $testDeps.Keys) {
        if ($direct.Contains($owner) -or $readers.Contains($owner)) { [void]$tests.Add($owner); continue }
        foreach ($d in $testDeps[$owner]) { if ($direct.Contains($d)) { [void]$tests.Add($owner); break } }
    }
    if ($tests.Count -gt 0) { foreach ($s in $smoke) { if ($testDeps.ContainsKey("$module/$s")) { [void]$tests.Add("$module/$s") } } }
    $testPkgs = @($tests | Sort-Object)
    $vetPkgs = @($vetSet | Sort-Object)
    # HTTP surface: handlers, routes and the server binary (other packages have their own tests)
    $serverCode = @($goChanged | Where-Object { -not $_.EndsWith('_test.go') -and (Test-Prefix @($_) $contractGoPrefixes) })
    $needGoTest = $testPkgs.Count -gt 0
    $needVet = $vetPkgs.Count -gt 0
    $needDocs = Test-Prefix $changed $docsPrefixes
    $needContract = ($serverCode.Count -gt 0) -or (Test-Prefix $changed $contractPrefixes)
    $needJs = Test-Prefix $changed $jsPrefixes
    $needBuild = ($direct.Count -gt 0) -or $needContract
    Write-Host "Quick gate: $($changed.Count) file(s) changed since $baseRev"
    Write-Host "  go test: $(if ($needGoTest) { ($testPkgs | ForEach-Object { $_.Substring($module.Length + 1) }) -join ' ' } else { 'none affected' })"
} else {
    Write-Host "Gate: every check ($why)"
}
# ---- checks -----------------------------------------------------------------------------
# The JS suite runs beside the Go steps; its output is printed when it ends.
$jsProc = $null
if ($needJs) {
    $jsOut = [IO.Path]::GetTempFileName(); $jsErr = [IO.Path]::GetTempFileName()
    $jsProc = Start-Process -FilePath 'node' -ArgumentList '--experimental-vm-modules', '--test', 'tests/js/' -WorkingDirectory $root `
        -NoNewWindow -PassThru -RedirectStandardOutput $jsOut -RedirectStandardError $jsErr
    $null = $jsProc.Handle  # keeps ExitCode readable after the process ends
    Write-Host "==> JS suite: started beside the Go steps (PID $($jsProc.Id))"
}

Push-Location $go
try {
    if (-not $selective) {
        Step 'gofmt' {
            $out = gofmt -l ./cmd ./internal
            if ($out) { Write-Host "gofmt needed:"; $out | ForEach-Object { Write-Host $_ }; $global:LASTEXITCODE = 1 }
        }
    } elseif ($fmtFiles.Count -gt 0) {
        Step "gofmt ($($fmtFiles.Count) changed file(s))" {
            $out = gofmt -l $fmtFiles
            if ($out) { Write-Host "gofmt needed:"; $out | ForEach-Object { Write-Host $_ }; $global:LASTEXITCODE = 1 }
        }
    } else { Skip 'gofmt' 'no changed .go files' }

    if (-not $selective) { Step 'go vet' { go vet ./... } }
    elseif ($needVet) { Step "go vet ($($vetPkgs.Count) changed package(s))" { go vet $vetPkgs } }
    else { Skip 'go vet' 'no changed Go packages' }

    if ($needDocs -or $needContract) {
        Step 'agent docs (generate, docs-check)' {
            go generate ./internal/agentdocs
            if ($LASTEXITCODE -eq 0 -and $needDocs) { go run ./cmd/ark-server docs-check --docs ../../docs }
        }
    } else { Skip 'agent docs' 'docs/ and agentdocs unchanged' }

    Use-TestPath
    try {
        if (-not $Quick) { Step "go test (uncached, -p $par)" { go test -count=1 -p $par ./... } }
        elseif (-not $selective) {
            # cached results are reused, except for packages whose tests read files outside the module
            $outside = @($goReads.Values | ForEach-Object { $_ } | Sort-Object -Unique | ForEach-Object { "./$_" })
            Step "go test (-p $par)" {
                go test -p $par ./...
                if ($LASTEXITCODE -eq 0) { go test -count=1 -p $par $outside }
            }
        }
        elseif ($needGoTest) {
            # Go's test cache ignores files outside the module (docs/, web/ ...): packages whose
            # tests read a changed one run uncached
            $fresh = @($testPkgs | Where-Object { $readers.Contains($_) -and -not $direct.Contains($_) })
            $rest = @($testPkgs | Where-Object { $fresh -notcontains $_ })
            Step "go test ($($testPkgs.Count) affected package(s), -p $par)" {
                if ($rest) { go test -p $par $rest }
                if ($LASTEXITCODE -eq 0 -and $fresh) { go test -count=1 -p $par $fresh }
            }
        }
        else { Skip 'go test' 'no Go package affected' }
    } finally { Restore-Path }

    if ($needBuild) { Step 'build ark-server' { go build -o $bin ./cmd/ark-server } }
    else { Skip 'build ark-server' 'no Go package affected' }
} finally { Pop-Location }

Push-Location $root
try {
    if ($needContract) {
        Step 'contract tools (go_only, fixtures)' {
            & $py -B -m unittest tests.python.contract.test_go_only
            if ($LASTEXITCODE -eq 0) { & $py -B tests/python/contract/record_fixtures.py --check }
        }
        Step 'contract harness (ark-server alone)' {
            $env:ARK_CONTRACT_SERVER = $bin
            try { & $py -B tests/python/contract/harness.py } finally { Remove-Item Env:ARK_CONTRACT_SERVER }
        }
    } else { Skip 'contract harness' 'server code and route lists unchanged' }
    if (-not $Quick -and -not (Test-Path "$root/tests/python/contract/goldens/local.json.gz")) {
        Write-Host '==> golden replay: skipped (no goldens in this tree; record them with golden.py --record)'
    } elseif (-not $Quick) {
        Step 'golden replay (ark-server alone)' { & $py -B tests/python/contract/golden.py --go $bin }
    }
    if ($jsProc) {
        $jsProc.WaitForExit()
        Write-Host '==> JS suite output'
        $keep = if ($jsProc.ExitCode -eq 0) { 12 } else { 100000 }
        Get-Content -LiteralPath $jsOut, $jsErr | Select-Object -Last $keep | ForEach-Object { Write-Host $_ }
        Remove-Item -LiteralPath $jsOut, $jsErr -ErrorAction SilentlyContinue
        $secs = [int]($jsProc.ExitTime - $jsProc.StartTime).TotalSeconds
        if ($jsProc.ExitCode -eq 0) { $results.Add("ok    JS suite (${secs}s, beside Go)") }
        else { $results.Add("FAIL  JS suite (${secs}s, exit $($jsProc.ExitCode))"); $script:failed = $true }
    } else { Skip 'JS suite' 'web/, desktop/ and tests/js unchanged' }
    if ($Legacy -and -not (Test-Path (Join-Path $root 'ark_studio/python/arkstudio'))) {
        Write-Host '==> legacy: skipped (no ark_studio/python in this tree)'
    } elseif ($Legacy) {
        Step 'legacy: differential replay vs Python' { & $py -B tests/python/contract/diffreplay.py --go $bin }
    }
} finally { Pop-Location }

# ---- pass stamp -------------------------------------------------------------------------
$stampNote = ''
if (-not $script:failed -and $stampDir) {
    $headNow = (git -C $root rev-parse --verify -q 'HEAD^{commit}' 2>$null)
    $dirtyNow = [bool](git -C $root status --porcelain 2>$null)
    $manifest = Join-Path $root 'ark_studio/web/manifest.webmanifest'
    $lf = (Test-Path -LiteralPath $manifest) -and -not ([IO.File]::ReadAllText($manifest).Contains("`r`n"))
    if (-not $head -or $dirtyAtStart -or $dirtyNow -or $headNow -ne $head) {
        $stampNote = 'no pass stamp (uncommitted changes or HEAD moved)'
    } else {
        $mode = if (-not $Quick) { 'full' } elseif ($selective) { 'quick' } else { 'quick-all' }
        $stamp = [ordered]@{
            revision = $head
            tree = (git -C $root rev-parse "$head^{tree}")
            scriptHash = $scriptHash
            goVersion = $goVersion
            mode = $mode
            base = $baseRev
            lf = $lf
            time = [DateTime]::UtcNow.ToString('o')
            checks = @($results)
        }
        $null = New-Item -ItemType Directory -Force -Path $stampDir
        $tmpStamp = Join-Path $stampDir "$head.json.tmp$PID"
        [IO.File]::WriteAllText($tmpStamp, ($stamp | ConvertTo-Json -Depth 3))
        Move-Item -LiteralPath $tmpStamp -Destination (Join-Path $stampDir "$head.json") -Force
        $stampNote = "pass stamp: $(Join-Path $stampDir "$head.json") ($mode)"
        Get-ChildItem -LiteralPath $stampDir -Filter '*.json' | Where-Object { $_.LastWriteTime -lt (Get-Date).AddDays(-14) } |
            Remove-Item -ErrorAction SilentlyContinue
    }
}

Write-Host ''
Write-Host "Backend gate summary ($([int]$total.Elapsed.TotalSeconds)s):"
if ($selective) { Write-Host "  compared with $baseRev ($($changed.Count) changed file(s))" } else { Write-Host "  every check ($why)" }
$results | ForEach-Object { Write-Host "  $_" }
if ($stampNote) { Write-Host "  $stampNote" }
if ($script:failed) { Write-Host 'FAIL'; exit 1 }
Write-Host 'PASS'
exit 0
