#!/usr/bin/env bash
# ScorpionAgent — production helper: clear build caches, rebuild, restart services, commit & push.
#
# Usage:
#   ./deploy.sh "your commit message"
#
# Environment (optional):
#   REMOTE_URL        — Git remote (default: https://github.com/Aboodseada1/ScorpionAgent.git)
#   SYSTEMD_UNITS     — space-separated systemd units to restart after build
#                       (default: agent-voice.service llama-qwen.service).
#                       Add your whisper.cpp unit if you use server STT, e.g.:
#                       SYSTEMD_UNITS="agent-voice.service llama-qwen.service whisper-server.service"

set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(pwd)"

MSG="${1:-ScorpionAgent auto deploy}"
REMOTE_URL="${REMOTE_URL:-https://github.com/Aboodseada1/ScorpionAgent.git}"
SYSTEMD_UNITS="${SYSTEMD_UNITS:-agent-voice.service llama-qwen.service}"

echo "==========================================="
echo "  ScorpionAgent — deploy"
echo "  (cache → build → restart → git push)"
echo "==========================================="

# 1. Clear caches (keep node_modules; drop Vite caches + old dist + Go binary from prior build)
echo ""
echo "[1/5] Clearing caches..."
rm -rf \
  "${ROOT}/bin" \
  "${ROOT}/web/dist" \
  "${ROOT}/web/node_modules/.cache" \
  "${ROOT}/web/node_modules/.vite"
find "${ROOT}/internal" -type d -name '__pycache__' -prune -exec rm -rf {} + 2>/dev/null || true
echo "       Done."

# 2. Build web + Go binary (see Makefile)
echo ""
echo "[2/5] Building (make all)..."
if ! command -v make >/dev/null 2>&1; then
  echo "       ERROR: make not in PATH"
  exit 1
fi
make -C "${ROOT}" all
echo "       Done."

# 3. Restart app services (systemd)
echo ""
echo "[3/5] Restarting systemd units..."
RESTARTED=0
for unit in ${SYSTEMD_UNITS}; do
  if systemctl cat "$unit" &>/dev/null; then
    if systemctl restart "$unit" 2>/dev/null || sudo systemctl restart "$unit"; then
      echo "       ${unit} OK."
      RESTARTED=1
    else
      echo "       WARN: could not restart ${unit} (try: sudo systemctl restart ${unit})"
    fi
  fi
done
if [[ "${RESTARTED}" -eq 0 ]]; then
  echo "       WARN: no known units were restarted (set SYSTEMD_UNITS or install services)."
fi
echo "       Done."

# Ensure git repo + origin
if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo ""
  echo "[git] Initializing repository..."
  git init -b main
fi

if git remote get-url origin >/dev/null 2>&1; then
  git remote set-url origin "${REMOTE_URL}"
else
  git remote add origin "${REMOTE_URL}"
fi

# 4. Commit
echo ""
echo "[4/5] Committing changes..."
git add -A
if git diff --cached --quiet; then
  echo "       No changes to commit — skipping."
else
  git commit -m "$MSG"
  echo "       Committed: $MSG"
fi

# 5. Push
echo ""
echo "[5/5] Pushing to origin..."
BRANCH="$(git branch --show-current)"
if [[ -z "${BRANCH}" ]]; then
  git branch -M main
  BRANCH=main
fi
if git push -u origin "${BRANCH}"; then
  echo "       Push OK."
else
  echo "       WARN: git push failed (check branch, auth, and GitHub remote)."
  exit 1
fi

echo ""
echo "==========================================="
echo "  ALL DONE"
echo "==========================================="
