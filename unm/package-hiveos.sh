#!/usr/bin/env bash
# Assemble the Hive OS custom-miner package: unm-hiveos.tar.gz  (UNM = Ultra Native Miner)
# The tarball bundles the SOURCE + build.sh; the binary is built on the rig at
# first start (best perf: -march=native). If you drop a prebuilt ./unm into
# hiveos/unm/ before packaging, h-run.sh will use it and skip the build.
#
#   bash package-hiveos.sh          # -> unm-hiveos.tar.gz
set -e
cd "$(dirname "$0")"

STAGE=hiveos/unm
SRC="nm_fast.c nm_fast.h nm_params.c nm_params.h nm_sha256.h nm_aes.h nm_neuromorph.h core_addr.h nmminer.c build.sh run-numa.sh"

echo "staging sources into $STAGE/"
for f in $SRC; do cp -f "$f" "$STAGE/"; done
# bundle the prebuilt static Linux binary so rigs run WITHOUT building (gcc not
# needed). h-run.sh uses ./unm if present, else builds from the bundled source.
if [ -f unm-linux-amd64 ]; then cp -f unm-linux-amd64 "$STAGE/unm"; chmod +x "$STAGE/unm"; echo "bundled prebuilt ./unm"; fi
chmod +x "$STAGE"/h-run.sh "$STAGE"/h-config.sh "$STAGE"/stats.sh "$STAGE"/build.sh "$STAGE"/run-numa.sh 2>/dev/null || true

echo "creating unm-hiveos.tar.gz"
tar -czf unm-hiveos.tar.gz -C hiveos unm
echo "done: $(pwd)/unm-hiveos.tar.gz"
echo "Upload it in Hive OS:  Flight Sheets -> Miners -> Add Miner -> Custom -> Install (URL or file)."
