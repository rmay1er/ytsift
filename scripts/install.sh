#!/usr/bin/env bash
# Builds the yt-insight Go binary from source and verifies that
# yt-dlp is available. Idempotent: safe to re-run.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$SCRIPT_DIR/yt-insight"

echo "==> building yt-insight"
cd "$SKILL_DIR/tool"
go build -trimpath -o "$BINARY" ./cmd/yt-insight
echo "    built: $BINARY"

if ! command -v yt-dlp >/dev/null 2>&1; then
  echo "==> yt-dlp not found on PATH"
  echo "    install it with one of:"
  echo "      brew install yt-dlp"
  echo "      pipx install yt-dlp"
  echo "      pip install --user yt-dlp"
  echo "    yt-dlp is required at runtime; install.sh will not install it"
  echo "    automatically because package managers differ per platform."
  exit 1
fi
echo "==> yt-dlp: $(yt-dlp --version | head -1)"
