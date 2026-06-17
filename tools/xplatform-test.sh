#!/bin/bash
# Verify NeuroMorph hashes identically on amd64, wasm and arm64.
export PATH=$PATH:/usr/local/go/bin:/usr/bin
cd /opt/cerebra/src
GOROOT=$(go env GOROOT)

echo "=== amd64 ==="
go test ./neuromorph/ -run TestCrossPlatformHash -v 2>&1 | grep -E 'PASS|FAIL|MISMATCH|ok|got|want'

echo "=== wasm (browser) ==="
if [ -f "$GOROOT/lib/wasm/go_js_wasm_exec" ]; then
  WEXEC="$GOROOT/lib/wasm/go_js_wasm_exec"
elif [ -f "$GOROOT/misc/wasm/go_js_wasm_exec" ]; then
  WEXEC="$GOROOT/misc/wasm/go_js_wasm_exec"
else
  WEXEC=""
fi
if [ -n "$WEXEC" ] && command -v node >/dev/null 2>&1; then
  GOOS=js GOARCH=wasm go test -exec="$WEXEC" ./neuromorph/ -run TestCrossPlatformHash -v 2>&1 | grep -E 'PASS|FAIL|MISMATCH|ok|got|want'
else
  echo "wasm exec or node not found (wexec=$WEXEC)"
fi

echo "=== arm64 (via qemu) ==="
if command -v qemu-aarch64-static >/dev/null 2>&1; then
  GOARCH=arm64 CGO_ENABLED=0 go test -exec=qemu-aarch64-static ./neuromorph/ -run TestCrossPlatformHash -v 2>&1 | grep -E 'PASS|FAIL|MISMATCH|ok|got|want'
else
  echo "qemu-aarch64-static not installed"
fi
