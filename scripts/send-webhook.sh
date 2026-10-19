#!/usr/bin/env sh
# Send a signed GitHub-style "pull_request opened" webhook to a running cowboy.
#   ./scripts/send-webhook.sh 512            (demo, default secret)
#   COWBOY=http://host:8080 WEBHOOK_SECRET=x ./scripts/send-webhook.sh 42 owner/repo
set -e
N=${1:?pr number}
REPO=${2:-acme/app}
URL=${COWBOY:-http://localhost:8080}
SECRET=${WEBHOOK_SECRET:-demo-secret}
BODY=$(printf '{"action":"synchronize","number":%s,"pull_request":{"number":%s},"repository":{"full_name":"%s"}}' "$N" "$N" "$REPO")
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')"
curl -sS -X POST "$URL/webhook/github" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-Hub-Signature-256: $SIG" \
  -H "Content-Type: application/json" \
  --data "$BODY"
