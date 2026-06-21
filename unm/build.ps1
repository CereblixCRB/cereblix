# Build nmminer on Windows with MSYS2 ucrt64 gcc.
# Usage:  powershell -ExecutionPolicy Bypass -File build.ps1
$ErrorActionPreference = "Stop"
$gccDir = "C:\msys64\ucrt64\bin"
if (-not (Test-Path "$gccDir\gcc.exe")) { throw "MSYS2 ucrt64 gcc not found at $gccDir. Install MSYS2 + mingw-w64-ucrt-x86_64-gcc." }
$env:PATH = "$gccDir;" + $env:PATH
Set-Location $PSScriptRoot

$cflags = @("-O3","-march=native","-maes","-mavx2","-funroll-loops","-fno-tree-vectorize","-ffp-contract=off")
Write-Host "building nmminer.exe ..."
& gcc @cflags -o nmminer.exe nmminer.c nm_fast.c nm_params.c -lpthread -lws2_32 -lm
if ($LASTEXITCODE -ne 0) { throw "build failed" }

Write-Host "building nmbench_fast.exe (correctness + offline bench) ..."
& gcc @cflags -o nmbench_fast.exe nmbench_fast.c nm_fast.c nm_params.c -lm
Write-Host "OK -> nmminer.exe, nmbench_fast.exe"
