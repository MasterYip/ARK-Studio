param([string]$Output = "")
$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if (-not $Output) { $Output = Join-Path $repo "ARK-Tray.exe" }
$compiler = Join-Path $env:WINDIR "Microsoft.NET/Framework64/v4.0.30319/csc.exe"
if (-not (Test-Path -LiteralPath $compiler)) {
    $compiler = Join-Path $env:WINDIR "Microsoft.NET/Framework/v4.0.30319/csc.exe"
}
if (-not (Test-Path -LiteralPath $compiler)) { throw "The .NET Framework C# compiler is required." }
$source = Join-Path $PSScriptRoot "windows-launcher.cs"
$icon = Join-Path $repo "web\brand\ark.ico"
$Output = [IO.Path]::GetFullPath($Output)
$edge = @(
    "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
    "${env:ProgramFiles}\Microsoft\Edge\Application\msedge.exe"
) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
$svg = Join-Path $repo "web\brand\ark-lockup-corner.svg"
$lockupPng = Join-Path $repo "web\brand\ark-lockup-corner.png"
if ($edge -and (Test-Path -LiteralPath $svg)) {
    $svgUri = ([Uri]$svg).AbsoluteUri
    $renderArgs = @("--headless", "--disable-gpu", "--hide-scrollbars", "--allow-file-access-from-files",
        "--default-background-color=00000000", "--window-size=141,30",
        "--virtual-time-budget=300", "--screenshot=$lockupPng", $svgUri)
    $render = Start-Process -FilePath $edge -ArgumentList $renderArgs -Wait -PassThru -WindowStyle Hidden
    if ($render.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $lockupPng)) {
        throw "Could not rasterize the ARK lockup SVG for the native chooser."
    }
}
& $compiler /nologo /noconfig /target:winexe /platform:anycpu /optimize+ /debug- /reference:System.dll /reference:System.Core.dll /reference:System.Windows.Forms.dll "/win32icon:$icon" "/out:$Output" $source
if ($LASTEXITCODE -ne 0) { throw "ARK launcher compilation failed." }
Get-Item -LiteralPath $Output | Select-Object FullName,Length
