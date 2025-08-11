#!/usr/bin/env bash
set -euo pipefail
FILE_PATH="${1:-}"
echo "[example] CODE=${CODE:-}"
echo "[example] UPLOAD_FILE=${FILE_PATH}"
if [[ -n "$FILE_PATH" && -f "$FILE_PATH" ]]; then
  echo "文件大小: $(wc -c < "$FILE_PATH") bytes"
  echo "前80字节:"
  head -c 80 "$FILE_PATH" | hexdump -C || true
fi
echo "执行完成"


