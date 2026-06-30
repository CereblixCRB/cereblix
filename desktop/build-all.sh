#!/usr/bin/env sh
# build-all.sh — cross-platform build notes & helper for the Cereblix Wallet.
#
# The wallet is a Wails v2 app. Wails uses each OS's NATIVE webview
# (WebView2 on Windows, WKWebView on macOS, WebKit2GTK on Linux), so the GUI
# binary CANNOT be cross-compiled across operating systems: you must build each
# target ON that target OS (or in a matching CI runner / container).
#
#   Windows  -> build on Windows      (see build-windows.ps1)
#   Linux    -> build on Linux
#   macOS    -> build on macOS ONLY   (Apple frameworks + codesign live there)
#
# The frontend is pure vanilla HTML/CSS/JS — there is NO `frontend:install` /
# `frontend:build` step (no npm, no bundler). `wails build` just embeds the
# frontend dir into the Go binary.
#
# Usage:  ./build-all.sh [linux|windows|mac|help]
set -eu

TARGET="${1:-help}"

build_linux() {
    # Runtime/build deps (Debian/Ubuntu names):
    #   sudo apt-get install -y build-essential pkg-config \
    #        libgtk-3-dev libwebkit2gtk-4.0-dev
    # (Fedora: gtk3-devel webkit2gtk4.1-devel ; Arch: gtk3 webkit2gtk)
    echo ">> building linux/amd64"
    CGO_ENABLED=1 wails build -clean -platform linux/amd64 -o cereblix-wallet
    echo "OK -> build/bin/cereblix-wallet"
}

build_windows() {
    echo ">> on Windows, run the PowerShell script instead:"
    echo "   powershell -ExecutionPolicy Bypass -File build-windows.ps1"
    echo "   (sets the pinned Go + ucrt64 gcc, then: wails build -clean -platform windows/amd64)"
}

build_mac() {
    # Must run on macOS. Universal binary covers Intel + Apple Silicon.
    # Optional signing/notarization: wails build ... then codesign/notarytool.
    echo ">> building darwin/universal (macOS host required)"
    wails build -clean -platform darwin/universal -o cereblix-wallet
    echo "OK -> build/bin/Cereblix Wallet.app"
}

case "$TARGET" in
    linux)   build_linux ;;
    windows) build_windows ;;
    mac)     build_mac ;;
    *)
        cat <<'EOF'
Cereblix Wallet — cross-platform build

Build each platform on its own OS (native webview cannot be cross-compiled):

  Windows :  powershell -ExecutionPolicy Bypass -File build-windows.ps1
  Linux   :  ./build-all.sh linux      (needs gtk3 + webkit2gtk dev packages)
  macOS   :  ./build-all.sh mac        (must run on a Mac; produces a .app)

Output: build/bin/

Prereqs everywhere:
  - Go 1.25+              (this repo pins toolchain/go)
  - wails CLI v2.12       (go install github.com/wailsapp/wails/v2/cmd/wails@latest)
  - a C compiler          (gcc/clang; Windows uses ucrt64 gcc)
  - NO Node.js / npm      (frontend is vanilla; nothing to install or bundle)
EOF
        ;;
esac
