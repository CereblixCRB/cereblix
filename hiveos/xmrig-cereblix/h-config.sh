#!/usr/bin/env bash
# xmrig-cereblix HiveOS config generator.
#
# Algo is FIXED to nm/1 - you do NOT need to put anything in
# "Extra config arguments". Just set the wallet and pool URL in the flight
# sheet. Pool URL defaults to stratum.cereblix.com:3333 if left empty.
#
# "Extra config arguments" (CUSTOM_USER_CONFIG), if used, must be JSON config
# fragments (e.g.  "huge-pages": false ), one per line - NOT CLI flags like
# "-a nm/1". Non-JSON lines are ignored instead of breaking the config.

[[ -z $CUSTOM_URL ]] && CUSTOM_URL="stratum.cereblix.com:3333"

# base config (cpu/huge-pages etc.)
conf=$(cat $MINER_DIR/$CUSTOM_MINER/config_global.json)

# enable the http api (used by h-stats.sh) + log file
http=$(jq -n --arg port "$MINER_API_PORT" --arg log "$CUSTOM_LOG_BASENAME.log" \
	'{http:{enabled:true,host:"127.0.0.1",port:($port|tonumber),"access-token":null,restricted:true},"log-file":$log}')
conf=$(jq -s '.[0] * .[1]' <<< "$conf $http")

# build the pools array (one entry per URL); algo is hardcoded to nm/1
pools='[]'
for url in $CUSTOM_URL; do
	pool=$(jq -n --arg url "$url" --arg user "$CUSTOM_TEMPLATE" --arg pass "$CUSTOM_PASS" --arg rig "$WORKER_NAME" \
		'{algo:"nm/1",coin:null,url:$url,user:$user,pass:$pass,"rig-id":$rig,nicehash:false,keepalive:true,enabled:true,tls:false}')
	pools=$(jq -n --argjson pools "$pools" --argjson pool "$pool" '$pools + [$pool]')
done
conf=$(jq -s '.[0] * .[1]' <<< "$conf $(jq -n --argjson p "$pools" '{pools:$p}')")

# optional user JSON config lines; CLI-style args are invalid here and skipped
if [[ ! -z $CUSTOM_USER_CONFIG ]]; then
	while read -r line; do
		[[ -z $line ]] && continue
		merged=$(jq -s '.[0] * .[1]' <<< "$conf {$line}" 2>/dev/null)
		if [[ -n $merged ]]; then
			conf=$merged
		else
			echo -e "${YELLOW}xmrig-cereblix: ignoring non-JSON extra config line: $line${NOCOLOR}"
		fi
	done <<< "$CUSTOM_USER_CONFIG"
fi

mkfile_from_symlink $CUSTOM_CONFIG_FILENAME
echo "$conf" | jq . > $CUSTOM_CONFIG_FILENAME
