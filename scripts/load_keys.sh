#!/usr/bin/env bash
# load_keys.sh — hot-import upstream API keys into onmi-routers at runtime.
#
# The gateway ships with ZERO upstream keys (they are imported via API, never
# stored in .gateway.env). This script pushes keys to the running gateway's
# import endpoints using the admin gateway key.
#
# USAGE:
#   ./scripts/load_keys.sh --keyfile keys.json --gwkey <GATEWAY_KEY> [--url https://onmi.my.id]
#
# KEYFILE JSON shape (all sections optional — only send what you have):
# {
#   "grok": [
#     { "email":"a@x.com", "access_token":"...", "refresh_token":"...", "expires_in":31535929 }
#   ],
#   "cb_api": [ "ck_xxx", "ck_yyy" ],
#   "cb_oauth": [
#     { "email":"a@x.com", "access_token":"...", "refresh_token":"..." }
#   ],
#   "cf": [ "account_id:token", "account_id2:token2" ]
# }
#
# Secrets are read from the keyfile ONLY — never hardcoded here. Keyfile stays
# local; do NOT commit it. The gateway key (--gwkey) is your admin bearer token.
set -euo pipefail

GWURL="https://onmi.my.id"
KEYFILE=""
GWKEY=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keyfile) KEYFILE="$2"; shift 2;;
    --gwkey)   GWKEY="$2";   shift 2;;
    --url)     GWURL="$2";   shift 2;;
    -h|--help) sed -n '2,30p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

if [[ -z "$KEYFILE" || -z "$GWKEY" ]]; then
  echo "ERROR: --keyfile and --gwkey are required" >&2
  exit 2
fi
if [[ ! -f "$KEYFILE" ]]; then
  echo "ERROR: keyfile not found: $KEYFILE" >&2
  exit 2
fi

post() { # $1=path $2=json-body
  curl -s -m 20 -H "Authorization: Bearer $GWKEY" -H "Content-Type: application/json" \
    -X POST "$GWURL$1" -d "$2" | sed 's/\(token.\{0,10\}\).*/\1***REDACTED***/'
  echo
}

echo "=== Grok accounts ==="
python3 - "$KEYFILE" <<'PY' | while IFS=$'\t' read -r body; do post /accounts/import "$body"; done
import json,sys
d=json.load(open(sys.argv[1]))
for a in d.get("grok",[]):
    print(json.dumps(a))
PY

echo "=== CodeBuddy API keys ==="
python3 - "$KEYFILE" <<'PY' | while IFS=$'\t' read -r raw; do
import json,sys
d=json.load(open(sys.argv[1]))
keys=d.get("cb_api",[])
if keys: print(json.dumps({"api_keys":keys}))
PY
post /cb/import "$(python3 - "$KEYFILE" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
keys=d.get("cb_api",[])
print(json.dumps({"api_keys":keys}) if keys else "{}")
PY
)"

echo "=== CodeBuddy OAuth accounts ==="
python3 - "$KEYFILE" <<'PY' | while IFS=$'\t' read -r body; do post /cb/oauth/import "$body"; done
import json,sys
d=json.load(open(sys.argv[1]))
for a in d.get("cb_oauth",[]):
    print(json.dumps(a))
PY

echo "=== Cloudflare accounts ==="
python3 - "$KEYFILE" <<'PY' | while IFS=$'\t' read -r k; do post /cf/import "$(python3 -c "import json,sys; print(json.dumps({'key':sys.argv[1]}))" "$k")"; done
import json,sys
d=json.load(open(sys.argv[1]))
for k in d.get("cf",[]):
    print(k)
PY

echo "=== DONE. Verify circuits at $GWURL/dashboard (Grok/CB/CF should show CLOSED with accounts loaded) ==="
