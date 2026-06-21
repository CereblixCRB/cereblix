#!/usr/bin/env bash
# One-command build of nmminer on Linux / HiveOS / any x86-64 box.
# Produces ./nmminer, tuned for THIS CPU (-march=native -> best on EPYC Zen4/Zen5).
#
#   ./build.sh            # build for this machine (recommended)
#   ./build.sh portable   # build a generic binary (-march=x86-64-v3, any Haswell/Zen+)
#
# Needs a C compiler with AES-NI + AVX2 (every Zen/Haswell-or-newer CPU has it).
set -e
cd "$(dirname "$0")"

# --- find or install a C compiler -------------------------------------------
CC=""
for c in cc gcc clang; do command -v "$c" >/dev/null 2>&1 && { CC="$c"; break; }; done
if [ -z "$CC" ]; then
  echo "no C compiler found - trying to install gcc..."
  if   command -v apt-get >/dev/null 2>&1; then sudo apt-get update -qq && sudo apt-get install -y gcc
  elif command -v dnf     >/dev/null 2>&1; then sudo dnf install -y gcc
  elif command -v yum     >/dev/null 2>&1; then sudo yum install -y gcc
  elif command -v pacman  >/dev/null 2>&1; then sudo pacman -Sy --noconfirm gcc
  elif command -v apk     >/dev/null 2>&1; then sudo apk add gcc musl-dev
  else echo "ERROR: install gcc manually (e.g. apt-get install gcc), then re-run."; exit 1
  fi
  CC=gcc
fi
echo "compiler: $CC ($($CC --version | head -1))"

# --- pick arch flags --------------------------------------------------------
if [ "$1" = "portable" ]; then ARCH="x86-64-v3"; else ARCH="native"; fi
COMMON="-O3 -maes -mavx2 -funroll-loops -fno-tree-vectorize -ffp-contract=off -pthread"
SRC="nmminer.c nm_fast.c nm_params.c"
LIBS="-lpthread -lm"

build() { echo "+ $CC -march=$1 $COMMON ..."; $CC -march="$1" $COMMON -o nmminer $SRC $LIBS; }

if build "$ARCH"; then :; else
  echo "march=$ARCH failed; falling back to -march=x86-64-v3"; build "x86-64-v3"
fi

echo
echo "built: ./nmminer"
echo "quick self-test (offline benchmark, 5s, all cores):"
./nmminer -bench 5 || true
echo
echo "next:  ./nmminer -node http://YOUR_NODE_IP:18751/api -addr crb1youraddr -lanes auto"
