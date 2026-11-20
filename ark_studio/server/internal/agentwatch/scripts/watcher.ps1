# ARK watcher (GET /api/agent/v1/watcher.ps1; downloading it needs no
# token), Windows PowerShell 5.1+.  Waits until a person replies in ARK on
# your units (comments, replies, Send back, decisions, ...) or changes the
# agent rules, prints what happened and exits.  Handle it, then start the
# watcher again.
#   env: ARK_URL, ARK_PROJECT; the token is read from ARK_TOKEN_FILE (default
#        %USERPROFILE%\.ark\tokens\<ARK_PROJECT>) or ARK_TOKEN and never
#        passed on a command line.
#   optional: ARK_AGENT_LABEL (your name in messages), ARK_HOST (this
#        device's id), ARK_WATCH_STATE (cursor file, default .ark-watch-state),
#        ARK_WATCH_MAX (seconds to wait, default 6900; 0 = check once),
#        ARK_OWNER_PID (the agent process this watcher belongs to).
#   Codex only: ARK_WAKE=codex and ARK_CODEX_THREAD=$env:CODEX_THREAD_ID.  The
#        watcher then checks that it runs under that Codex session, starts
#        itself in the background and wakes the session with `codex queue`.
# The watcher stops when the agent that started it is gone.
foreach ($n in 'ARK_URL', 'ARK_PROJECT') {
    if (-not [Environment]::GetEnvironmentVariable($n)) { Write-Output "set $n"; exit 2 }
}
$arkUrl = $env:ARK_URL.TrimEnd('/')

# ---------------------------------------------------------------- the token
$tf = if ($env:ARK_TOKEN_FILE) { $env:ARK_TOKEN_FILE } else { Join-Path $env:USERPROFILE ".ark\tokens\$($env:ARK_PROJECT)" }
$tok = ''
if (Test-Path -LiteralPath $tf) { $tok = "$(Get-Content -LiteralPath $tf -TotalCount 1)".Trim() }
$tokFromEnv = $false
if (-not $tok) { $tok = "$env:ARK_TOKEN".Trim(); $tokFromEnv = $true }
Remove-Item Env:ARK_TOKEN -ErrorAction SilentlyContinue
if (-not $tok) { Write-Output "ARK: no token: write it to $tf (only you may read it) or set ARK_TOKEN in this shell's environment."; exit 2 }
$headers = @{ Authorization = "Bearer $tok"; 'X-ARK-Project' = $env:ARK_PROJECT }
if ($env:ARK_HOST) { $headers['X-ARK-Host'] = $env:ARK_HOST }
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }
$curl = Get-Command curl.exe -ErrorAction SilentlyContinue
# Invoke-RestMethod first; curl.exe (Windows 10+) when .NET cannot open the
# connection (older TLS stacks).  curl.exe reads the headers from stdin
# (-K -), so the token is never in its arguments.
function Get-Ark([string]$uri, [int]$timeout) {
    try { return Invoke-RestMethod -Uri $uri -Headers $headers -TimeoutSec $timeout }
    catch {
        $resp = $_.Exception.Response
        if ($resp -and ([int]$resp.StatusCode -eq 401 -or [int]$resp.StatusCode -eq 403)) { $script:refused = [int]$resp.StatusCode; throw }
        if (-not $curl) { throw }
        $cfg = @(foreach ($k in $headers.Keys) { "header = `"${k}: $($headers[$k])`"" }) -join "`n"
        $raw = @($cfg | & $curl.Source -sS -K - -m "$timeout" -w "`n%{http_code}" $uri)
        $code = "$($raw[-1])".Trim()
        if ($code -eq '401' -or $code -eq '403') { $script:refused = [int]$code; throw "HTTP $code" }
        if ($LASTEXITCODE -ne 0 -or $code -ne '200') { throw "curl: HTTP $code (exit $LASTEXITCODE)" }
        return ($raw[0..($raw.Count - 2)] -join "`n") | ConvertFrom-Json
    }
}

# ---------------------------------------------------------------- the owner
function Get-Proc([int]$id) {
    Get-CimInstance Win32_Process -Filter "ProcessId=$id" -Property ProcessId, ParentProcessId, Name -ErrorAction SilentlyContinue
}
function Get-AgentKind($p) {
    if (-not $p) { return '' }
    switch -Regex ($p.Name) { '^claude(\.exe)?$' { return 'claude' } '^codex(\.exe)?$' { return 'codex' } }
    return ''
}
$owner = 0; $ownerKind = ''
if ($env:ARK_WATCH_DETACHED_OWNER) { $owner = [int]$env:ARK_WATCH_DETACHED_OWNER }
elseif ($env:ARK_OWNER_PID) { $owner = [int]$env:ARK_OWNER_PID }
if ($owner) { $ownerKind = Get-AgentKind (Get-Proc $owner) }
else { # the nearest agent among this watcher's ancestors
    $p = Get-Proc $PID; $seen = @{}
    for ($i = 0; $p -and $i -lt 40; $i++) {
        $p = Get-Proc $p.ParentProcessId
        if (-not $p -or $seen[$p.ProcessId]) { break }
        $seen[$p.ProcessId] = $true
        $k = Get-AgentKind $p
        if ($k) { $owner = [int]$p.ProcessId; $ownerKind = $k; break }
    }
    if (-not $owner) { $me = Get-Proc $PID; if ($me) { $owner = [int]$me.ParentProcessId } }
}
function Test-OwnerAlive {
    if (-not $owner) { return $true }
    $p = Get-Proc $owner
    if (-not $p) { return $false }
    return (-not $ownerKind) -or ((Get-AgentKind $p) -eq $ownerKind)
}

# ---------------------------------------------------------------- Codex wake-up
# Only when this Codex session asked for it (ARK_WAKE=codex with its own
# thread id), the nearest agent above the watcher is Codex, and the thread id
# Codex gave this command matches.  An inherited CODEX_THREAD_ID alone never
# wakes anything: it can belong to an unrelated session.
$codex = $null; $codexThread = ''
if ($env:ARK_WAKE -eq 'codex') {
    $why = ''
    if ($env:CLAUDECODE -or $env:CLAUDE_CODE_ENTRYPOINT) { $why = 'this is a Claude Code session' }
    elseif ($ownerKind -ne 'codex') { $why = 'no Codex process above the watcher' }
    elseif (-not $env:ARK_CODEX_THREAD -or $env:ARK_CODEX_THREAD -ne $env:CODEX_THREAD_ID) { $why = "ARK_CODEX_THREAD is not this Codex session's thread" }
    if (-not $why) {
        foreach ($c in @(Get-Command codex -All -ErrorAction SilentlyContinue)) {
            & $c.Source queue --help *> $null
            if ($LASTEXITCODE -eq 0) { $codex = $c; break }
        }
        if (-not $codex) { $why = 'no codex with `queue` found' }
    }
    if ($why) { Write-Output "ARK: not waking through codex queue ($why); the watcher exits on news instead." }
    else { $codexThread = $env:ARK_CODEX_THREAD }
}
if ($codexThread -and -not $env:ARK_WATCH_DETACHED_OWNER) {
    if (-not $PSCommandPath) { Write-Output 'ARK: save the watcher to a file (ark-watch.ps1) and run it with -File.'; exit 2 }
    $env:ARK_WATCH_DETACHED_OWNER = "$owner"
    if ($tokFromEnv) { $env:ARK_TOKEN = $tok }
    Start-Process powershell -WindowStyle Hidden -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`""
    Remove-Item Env:ARK_TOKEN -ErrorAction SilentlyContinue
    Write-Output 'ARK: the watcher runs in the background and wakes this Codex session through codex queue.'
    exit 0
}

# ---------------------------------------------------------------- the feed
$projectName = $env:ARK_PROJECT
try { $ctx = Get-Ark "$arkUrl/api/agent/v1/context" 20; if ($ctx.project.name) { $projectName = "$($ctx.project.name) ($($env:ARK_PROJECT))" } } catch { }
$script:refused = 0
$label = if ($env:ARK_AGENT_LABEL) { $env:ARK_AGENT_LABEL } elseif ($codexThread) { "Codex thread $codexThread" } else { 'the agent that started this watcher' }
$state = if ($env:ARK_WATCH_STATE) { $env:ARK_WATCH_STATE } else { '.ark-watch-state' }
$max = if ($env:ARK_WATCH_MAX) { [int]$env:ARK_WATCH_MAX } elseif ($codexThread) { 31536000 } else { 6900 }
$cursor = ''; $rules = ''
if (Test-Path $state) {
    $lines = @(Get-Content $state)
    if ($lines.Count -gt 0) { $cursor = "$($lines[0])".Trim() }
    if ($lines.Count -gt 1) { $rules = "$($lines[1])".Trim() }
}
$start = Get-Date
while ($true) {
    if (-not (Test-OwnerAlive)) { Write-Output "ARK: the agent that started this watcher ($ownerKind pid $owner) is gone; the watcher stops."; exit 0 }
    $left = $max - [int]((Get-Date) - $start).TotalSeconds
    $wait = [Math]::Max(0, [Math]::Min(50, $left))
    $q = "mine=1&wait=$wait"
    if ($cursor) { $q += "&since=$cursor" }
    try {
        $r = Get-Ark "$arkUrl/api/agent/v1/updates?$q" ($wait + 20)
    } catch {
        if ($script:refused) { Write-Output "ARK: the token was refused (HTTP $($script:refused)); the watcher stops."; exit 3 }
        if ($max -eq 0) { Write-Output "ARK: the watcher could not reach ARK: $($_.Exception.Message)"; exit 1 }
        Start-Sleep -Seconds 30
        continue
    }
    if ($r.cursor) { $cursor = $r.cursor }
    $changed = [bool]($rules -and $r.rules_version -and ($r.rules_version -ne $rules))
    if ($r.rules_version) { $rules = $r.rules_version }
    Set-Content -Path $state -Value @($cursor, $rules)
    if ($r.count -gt 0 -or $changed) {
        $note = if ($changed) { "; the agent rules changed to $rules (GET /api/agent/v1/rules)" } else { '' }
        $head = "ARK UPDATE for $label, ARK project $projectName at ${arkUrl}: $($r.count) new update(s)$note."
        Write-Output "$head Act on them (the items below, or GET /api/agent/v1/updates), then start this watcher again."
        $lines = @(foreach ($it in @($r.items)) { if ($it) { "- $($it.summary)  [$($it.link)]" } })
        $lines | ForEach-Object { Write-Output $_ }
        if ($codex) {
            $msg = "$head`n$($lines -join "`n")`nThis message comes from the ARK watcher you started for this project. If you are not working on ARK project $projectName, ignore it. Otherwise act on the updates under the core rules (reply with reply_to, resolve), then start the ARK watcher again."
            & $codex.Source queue --thread $codexThread --message $msg | Out-Null
            if ($LASTEXITCODE -ne 0) { Write-Output "ARK: codex queue failed; read GET /api/agent/v1/updates yourself." }
        }
        exit 0
    }
    if (((Get-Date) - $start).TotalSeconds -ge $max) { Write-Output "ARK: no updates in $max s; start the watcher again."; exit 0 }
}
