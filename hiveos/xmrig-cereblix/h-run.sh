#!/usr/bin/env bash
cd "$(dirname "$0")"

hugepages -rx

exec ./xmrig-cereblix -c config.json
