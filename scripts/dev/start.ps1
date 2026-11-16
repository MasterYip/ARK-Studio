param(
  [int]$Port = 8890,
  [string]$Root = "",
  [switch]$NoBrowser
)
$ErrorActionPreference = "Stop"
$task = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Set-Location $task
$cliArgs = @("-m", "arkstudio", "serve", "--port", "$Port")
if ($Root) { $cliArgs += @("--root", $Root) }
if (-not $NoBrowser) {
  Start-Job -ScriptBlock {
    param($url)
    Start-Sleep -Seconds 2
    Start-Process $url
  } -ArgumentList "http://127.0.0.1:$Port/" | Out-Null
}
python @cliArgs
