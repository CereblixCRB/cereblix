# build-windows.ps1 — build the Cereblix Wallet for Windows (amd64).
#
# Produces a single self-contained executable: build\bin\cereblix-wallet.exe
# No npm, no bundler, no node_modules — the frontend is pure vanilla HTML/CSS/JS
# embedded into the binary via //go:embed.
#
# Run this from anywhere; it cd's to its own directory (the desktop module root,
# where wails.json lives).

$ErrorActionPreference = "Stop"

# --- pinned local toolchain (do not rely on PATH) --------------------------
$Go    = "C:\Users\Lisa\Desktop\Cereblix\toolchain\go\bin\go.exe"
$Wails = Join-Path $env:USERPROFILE "go\bin\wails.exe"
$Gcc   = "C:\msys64\ucrt64\bin\gcc.exe"

# --- sanity checks ---------------------------------------------------------
foreach ($p in @($Go, $Wails, $Gcc)) {
    if (-not (Test-Path $p)) {
        Write-Error "required tool not found: $p"
        exit 1
    }
}

# Build from the module root (this script's folder).
Set-Location -Path $PSScriptRoot

# --- environment -----------------------------------------------------------
# Put our Go + the ucrt64 toolchain FIRST on PATH so the wails CLI invokes the
# correct go and gcc. CGO is enabled so the (optional) embedded full node and
# wails native bits compile with our gcc.
$GoBin   = Split-Path $Go
$GccBin  = Split-Path $Gcc
$env:PATH         = "$GoBin;$GccBin;$env:PATH"
$env:CC           = $Gcc
$env:CGO_ENABLED  = "1"
$env:GOTOOLCHAIN  = "local"   # never auto-download a different Go

Write-Host "Go    : $(& $Go version)"
Write-Host "Wails : $Wails"
Write-Host "CC    : $env:CC"
Write-Host ""

# --- build -----------------------------------------------------------------
# -clean wipes build/bin first. WebView2 strategy is the default 'download'
# (a tiny bootstrapper); on Windows 11 the WebView2 runtime is already present,
# so end users just double-click the .exe. To bundle the runtime offline add
# `-webview2 embed`.
& $Wails build -clean -platform windows/amd64 -o cereblix-wallet.exe
if ($LASTEXITCODE -ne 0) {
    Write-Error "wails build failed (exit $LASTEXITCODE)"
    exit $LASTEXITCODE
}

Write-Host ""
Write-Host "OK -> $(Join-Path $PSScriptRoot 'build\bin\cereblix-wallet.exe')"
