#!/usr/bin/env bash
# Cross-build the Linux / HiveOS binary with ONE toolchain (Zig) - works the same
# on a Windows laptop (Git Bash) and on Ubuntu. HiveOS is x86-64 Linux, so the
# Linux binary IS the HiveOS binary. Output: ./nmminer-linux-amd64 (musl-static,
# runs on ANY distro/glibc + HiveOS, no dependencies).
#
#   bash cross-build.sh
#
# Zig is a single ~80 MB download, no install/root. If it's not already next to
# this script (or on PATH), this script fetches it for the current OS.
set -e
cd "$(dirname "$0")"
ZIGVER=0.13.0

find_zig(){
  local z
  for z in ./zig-*/zig.exe ./zig-*/zig; do [ -x "$z" ] && { echo "$z"; return; }; done
  command -v zig >/dev/null 2>&1 && { echo zig; return; }
  echo ""
}

ZIG=$(find_zig)
if [ -z "$ZIG" ]; then
  host=$(uname -s 2>/dev/null || echo unknown)
  case "$host" in
    Linux)
      pkg="zig-linux-x86_64-$ZIGVER.tar.xz"
      echo "Zig not found - downloading for Linux ($pkg)..."
      curl -fsSL "https://ziglang.org/download/$ZIGVER/$pkg" -o "$pkg"
      tar -xf "$pkg"
      ZIG="./zig-linux-x86_64-$ZIGVER/zig" ;;
    MINGW*|MSYS*|CYGWIN*)
      pkg="zig-windows-x86_64-$ZIGVER.zip"
      echo "Zig not found - downloading for Windows ($pkg)..."
      curl -fsSL "https://ziglang.org/download/$ZIGVER/$pkg" -o "$pkg"
      command -v unzip >/dev/null 2>&1 && unzip -q "$pkg" || powershell -Command "Expand-Archive -Force '$pkg' ."
      ZIG="./zig-windows-x86_64-$ZIGVER/zig.exe" ;;
    *)
      echo "Could not auto-download Zig for '$host'. Grab it from https://ziglang.org/download/ and re-run."
      exit 1 ;;
  esac
fi
echo "using zig: $ZIG ($("$ZIG" version))"

# v3 baseline (AVX2/FMA/BMI) + AES; VAES-256/512 are added per-function and chosen
# at runtime, so this one binary is fast on Zen2..Zen5 and modern Intel alike.
CF="-O3 -mcpu=x86_64_v3+aes -ffp-contract=off -fno-vectorize -fno-slp-vectorize -funroll-loops"

echo "building nmminer-linux-amd64 (musl static)..."
"$ZIG" cc -target x86_64-linux-musl $CF -o nmminer-linux-amd64 nmminer.c nm_fast.c nm_params.c -lpthread -lm
echo
echo "done -> ./nmminer-linux-amd64   (copy to any Linux box or HiveOS rig)"
echo "verify on a Linux box:  ./nmminer-linux-amd64 -bench 10"
echo
echo "Windows binary: build with MSYS2 gcc (build.ps1) - Zig's mingw lacks winpthreads."
