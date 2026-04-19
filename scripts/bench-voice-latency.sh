#!/usr/bin/env bash
# Quick STT + LLM latency probe against local whisper + llama-server.
# Requires: curl, python3, a short WAV at /opt/whisper.cpp/samples/jfk.wav (or pass WAV path).
set -euo pipefail
WHISPER="${WHISPER_BASE_URL:-http://127.0.0.1:8082}"
LLM="${LLM_BASE_URL:-http://127.0.0.1:18080/v1}"
WAV="${1:-/opt/whisper.cpp/samples/jfk.wav}"
if [[ ! -f "$WAV" ]]; then
  echo "Missing WAV: $WAV"
  exit 1
fi

echo "=== Whisper STT ($WHISPER/inference) ==="
STT_START=$(date +%s%3N)
JSON=$(curl -sS -m 120 -X POST "$WHISPER/inference" \
  -F "file=@${WAV};type=audio/wav" \
  -F "response_format=json" \
  -F "temperature=0.0" \
  -F "language=en" \
  -F "model=unused")
STT_END=$(date +%s%3N)
echo "$JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print('text:', d.get('text','')[:120])" 2>/dev/null || echo "$JSON"
echo "wall_ms: $((STT_END - STT_START))"

echo
echo "=== LLM first tokens ($LLM/chat/completions) ==="
BODY='{"model":"local","messages":[{"role":"user","content":"Say hello in exactly five words."}],"max_tokens":40,"stream":true,"temperature":0.2}'
LLM_START=$(date +%s%3N)
# Time until first SSE data line (read until first line starting with "data:")
while IFS= read -r line; do
  case "$line" in
    data:*) break ;;
  esac
done < <(curl -sS -N -m 60 -H "Content-Type: application/json" -H "Accept: text/event-stream" \
  -X POST "$LLM/chat/completions" -d "$BODY" 2>/dev/null)
LLM_FIRST=$(date +%s%3N)
echo "first_sse_ms: $((LLM_FIRST - LLM_START))"

echo "Done."
