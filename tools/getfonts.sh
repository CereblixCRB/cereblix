#!/bin/bash
set -e
mkdir -p /var/www/html/cereblix/fonts
cd /var/www/html/cereblix/fonts
base="https://cdn.jsdelivr.net/fontsource/fonts"
declare -A f=(
  [spacegrotesk-500]="space-grotesk@latest/latin-500-normal.woff2"
  [spacegrotesk-700]="space-grotesk@latest/latin-700-normal.woff2"
  [inter-400]="inter@latest/latin-400-normal.woff2"
  [inter-500]="inter@latest/latin-500-normal.woff2"
  [inter-700]="inter@latest/latin-700-normal.woff2"
)
for name in "${!f[@]}"; do
  curl -fsSL -o "$name.woff2" "$base/${f[$name]}"
  echo "$name.woff2 $(stat -c%s "$name.woff2") bytes"
done
ls -la
