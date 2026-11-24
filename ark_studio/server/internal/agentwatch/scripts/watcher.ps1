# ARK watcher (GET /api/agent/v1/watcher.ps1), Windows PowerShell 5.1+.
# Prefer `ark-server agent watch --until-update`, which does the same.  To
# use this script, save it once with your file-writing tool as
# ark-watch.ps1 and run the saved file:
#   powershell -NoProfile -ExecutionPolicy Bypass -File ark-watch.ps1
# Waits until a person replies in ARK on your units (comments, replies,
# Send back, decisions, ...) or changes the agent rules, prints what
# happened and exits.  Handle it, then start the watcher again.
#   env: ARK_URL, ARK_PROJECT; the token is read from ARK_TOKEN_FILE (default
#        %USERPROFILE%\.ark\tokens\<ARK_PROJECT>) or ARK_TOKEN and handed to
#        curl.exe on its standard input, never on a command line.
#   optional: ARK_OWNER_PID (your agent's process id: the watcher stops when
#        it is gone; default: the nearest Codex or Claude Code process above
#        the watcher), ARK_AGENT_LABEL (your name in messages), ARK_HOST (this
#        device's id), ARK_WATCH_STATE (cursor file, default .ark-watch-state),
#        ARK_WATCH_MAX (seconds to wait, default 6900; 0 = check once).
#   Codex only: ARK_WAKE=codex and ARK_CODEX_THREAD=$env:CODEX_THREAD_ID.
#        The watcher then runs in the background and wakes the session with
#        `codex queue`; when it cannot, it says why and exits 2.
# It does not start in a session ARK launched and wakes itself
# (ARK_SESSION_ID with ARK_WAKE=ark).
if ($env:ARK_SESSION_ID -and $env:ARK_WAKE -eq 'ark') {
    Write-Output "ARK: ARK launched this session ($($env:ARK_SESSION_ID)) and wakes it itself when people reply; no watcher needed."
    exit 0
}
foreach ($n in 'ARK_URL', 'ARK_PROJECT') {
    if (-not [Environment]::GetEnvironmentVariable($n)) { Write-Output "set $n"; exit 2 }
}
$arkUrl = $env:ARK_URL.TrimEnd('/')
$curl = Get-Command curl.exe -ErrorAction SilentlyContinue
if (-not $curl) { Write-Output 'ARK: curl.exe not found (Windows 10 1803+ has it); use ark-server agent watch instead.'; exit 2 }

# ---------------------------------------------------------------- the token
$tf = if ($env:ARK_TOKEN_FILE) { $env:ARK_TOKEN_FILE } else { Join-Path $env:USERPROFILE ".ark\tokens\$($env:ARK_PROJECT)" }
$tok = ''
$tokFromEnv = $false
# In a session ARK launched, its own token (ARK_TOKEN) comes before the file.
if ($env:ARK_SESSION_ID -and "$env:ARK_TOKEN".Trim() -and -not $env:ARK_TOKEN_FILE) { $tok = "$env:ARK_TOKEN".Trim(); $tokFromEnv = $true }
elseif (Test-Path -LiteralPath $tf) { $tok = "$(Get-Content -LiteralPath $tf -TotalCount 1)".Trim() }
if (-not $tok) { $tok = "$env:ARK_TOKEN".Trim(); $tokFromEnv = $true }
Remove-Item Env:ARK_TOKEN -ErrorAction SilentlyContinue
if (-not $tok) { Write-Output "ARK: no token: write it to $tf (only you may read it) or set ARK_TOKEN in this shell's environment."; exit 2 }
$cfg = @("header = `"Authorization: Bearer $tok`"", "header = `"X-ARK-Project: $($env:ARK_PROJECT)`"")
if ($env:ARK_HOST) { $cfg += "header = `"X-ARK-Host: $($env:ARK_HOST)`"" }
if ($env:ARK_SESSION_ID) { $cfg += "header = `"X-ARK-Session: $($env:ARK_SESSION_ID)`"" }
$cfg = $cfg -join "`n"
# One request: curl.exe reads the headers from stdin (-K -).  Returns the
# HTTP status and sets $script:body.
function Invoke-Ark([string]$path, [int]$timeout) {
    $raw = @($cfg | & $curl.Source -sS -K - -m "$timeout" -w "`n%{http_code}" "$arkUrl$path" 2>$null)
    if ($raw.Count -eq 0) { $script:body = ''; return '000' }
    $script:body = ($raw[0..([Math]::Max(0, $raw.Count - 2))] -join "`n")
    if ($raw.Count -eq 1) { $script:body = '' }
    return "$($raw[-1])".Trim()
}

# ---------------------------------------------------------------- the owner
# The agent's process: ARK_OWNER_PID when it is Codex or Claude Code, or a
# live process that is not above this watcher.  Otherwise (gone, the shell
# that ran this command, or not set) the nearest codex or claude process
# above the watcher, else its nearest node process (a CLI wrapper).  Only
# parent ids and process names are read.
function Get-AgentKind([string]$n) {
    $n = $n.ToLower() -replace '\.exe$', ''
    if ($n -eq 'codex' -or $n -like 'codex-linux-san*') { return 'codex' }
    if ($n -eq 'claude') { return 'claude' }
    if ($n -eq 'node' -or $n -eq 'bun') { return 'node' }
    return ''
}
$owner = 0; $ownerKind = ''
if ($env:ARK_WATCH_DETACHED_OWNER) { $owner = [int]$env:ARK_WATCH_DETACHED_OWNER }
else {
    if ($env:ARK_OWNER_PID) { $owner = [int]$env:ARK_OWNER_PID }
    $procs = @{}
    try { Get-CimInstance Win32_Process -Property ProcessId, ParentProcessId, Name -ErrorAction Stop | ForEach-Object { $procs[[int]$_.ProcessId] = $_ } } catch { }
    if ($procs.Count) {
        $chain = @(); $seen = @{}; $p = [int]$procs[[int]$PID].ParentProcessId
        while ($p -ge 1 -and $procs.ContainsKey($p) -and -not $seen[$p] -and $chain.Count -lt 64) { $seen[$p] = 1; $chain += $p; $p = [int]$procs[$p].ParentProcessId }
        $keep = $false
        if ($owner -gt 1 -and $procs.ContainsKey($owner)) {
            $k = Get-AgentKind $procs[$owner].Name
            $keep = ($k -eq 'codex' -or $k -eq 'claude' -or $chain -notcontains $owner)
        }
        if (-not $keep) {
            $found = 0; $wrapper = 0
            foreach ($c in $chain) {
                $k = Get-AgentKind $procs[$c].Name
                if ($k -eq 'codex' -or $k -eq 'claude') { $found = $c; break }
                if ($k -eq 'node' -and -not $wrapper) { $wrapper = $c }
            }
            if (-not $found) { $found = $wrapper }
            if ($found) {
                if ($owner -gt 1) { Write-Output "ARK: pid $owner is gone or not your agent; the watcher belongs to $($procs[$found].Name) pid $found, the nearest agent above it." }
                $owner = $found
            }
        }
        if ($owner -gt 1 -and $procs.ContainsKey($owner)) { $ownerKind = Get-AgentKind $procs[$owner].Name }
    }
}
if ($owner -le 1) { $owner = 0 }
function Get-OwnerName { if (-not $owner) { return '' }; $p = Get-Process -Id $owner -ErrorAction SilentlyContinue; if ($p) { return $p.ProcessName } else { return $null } }
if ($owner -and $null -eq (Get-OwnerName)) { Write-Output "ARK: no process $owner (ARK_OWNER_PID)"; exit 2 }
if (-not $ownerKind -and $owner) { $ownerKind = Get-AgentKind (Get-OwnerName) }

# ---------------------------------------------------------------- Codex wake-up
# Only when this Codex session asked for it (ARK_WAKE=codex with its own
# thread id), the owner process is Codex and the thread id is the one Codex
# gave this command.  An inherited CODEX_THREAD_ID alone never wakes anything.
$codex = $null; $codexThread = ''
if ($env:ARK_WAKE -eq 'codex') {
    $why = ''
    if ($env:CLAUDECODE -or $env:CLAUDE_CODE_ENTRYPOINT) { $why = 'this is a Claude Code session' }
    elseif (-not $owner) { $why = 'owner process not found' }
    elseif ($ownerKind -ne 'codex') { $why = "the owner process (pid $owner) is not Codex" }
    elseif (-not $env:ARK_CODEX_THREAD -or $env:ARK_CODEX_THREAD -ne $env:CODEX_THREAD_ID) { $why = "ARK_CODEX_THREAD is not this Codex session's thread" }
    if (-not $why) {
        foreach ($c in @(Get-Command codex -All -ErrorAction SilentlyContinue)) {
            & $c.Source queue --help *> $null
            if ($LASTEXITCODE -eq 0) { $codex = $c; break }
        }
        if (-not $codex) { $why = 'no codex with `queue` found' }
    }
    if ($why) {
        # Say it and fail: a watcher that cannot wake Codex is no use.
        $hint = if ($why -like '*Claude Code*') { 'Claude Code is woken when the watcher exits: drop ARK_WAKE' } elseif ($why -like '*thread*') { 'set ARK_CODEX_THREAD=$env:CODEX_THREAD_ID from your own session' } elseif ($why -like '*queue*') { 'update Codex' } else { 'set ARK_OWNER_PID=<codex pid>' }
        Write-Output "ARK: cannot wake codex: $why; $hint"
        exit 2
    }
    $codexThread = $env:ARK_CODEX_THREAD
}
if ($codexThread -and -not $env:ARK_WATCH_DETACHED_OWNER) {
    if (-not $PSCommandPath) { Write-Output 'ARK: save the watcher as ark-watch.ps1 and run it with -File.'; exit 2 }
    $env:ARK_WATCH_DETACHED_OWNER = "$owner"
    if ($tokFromEnv) { $env:ARK_TOKEN = $tok }
    Start-Process powershell -WindowStyle Hidden -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`""
    Remove-Item Env:ARK_TOKEN -ErrorAction SilentlyContinue
    Write-Output 'ARK: the watcher runs in the background and wakes this Codex session through codex queue.'
    exit 0
}

# ---------------------------------------------------------------- the feed
$projectName = $env:ARK_PROJECT
if ((Invoke-Ark '/api/agent/v1/context' 20) -eq '200') {
    try { $ctx = $script:body | ConvertFrom-Json; if ($ctx.project.name) { $projectName = "$($ctx.project.name) ($($env:ARK_PROJECT))" } } catch { }
}
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
    if ($owner -and $null -eq (Get-OwnerName)) { Write-Output "ARK: the agent that started this watcher (pid $owner) is gone; the watcher stops."; exit 0 }
    $left = $max - [int]((Get-Date) - $start).TotalSeconds
    $wait = [Math]::Max(0, [Math]::Min(50, $left))
    $q = "mine=1&wait=$wait"
    if ($cursor) { $q += "&since=$cursor" }
    $code = Invoke-Ark "/api/agent/v1/updates?$q" ($wait + 20)
    if ($code -eq '401' -or $code -eq '403') { Write-Output "ARK: the token was refused (HTTP $code); the watcher stops."; exit 3 }
    $r = $null
    if ($code -eq '200') { try { $r = $script:body | ConvertFrom-Json } catch { $r = $null } }
    if (-not $r) {
        if ($max -eq 0) { Write-Output "ARK: the watcher could not reach ARK (HTTP $code)"; exit 1 }
        Start-Sleep -Seconds 30
        continue
    }
    if ($r.cursor) { $cursor = $r.cursor }
    $changed = [bool]($rules -and $r.rules_version -and ($r.rules_version -ne $rules))
    if ($r.rules_version) { $rules = $r.rules_version }
    Set-Content -Path $state -Value @($cursor, $rules)
    if ($r.count -gt 0 -or $changed) {
        $note = if ($changed) { "; the agent rules changed to $rules (GET /api/agent/v1/rules)" } else { '' }
        $head = "ARK UPDATE for $label, ARK project ${projectName}: $($r.count) new update(s)$note."
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
