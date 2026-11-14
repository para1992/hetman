#!/bin/sh
# Writes the release and reports it as an artifact through the result file.
set -eu
cp "$HETMAN_RUN_DIR/build.txt" "$HETMAN_RUN_DIR/release.txt"
printf '{"outcome": "ok", "artifacts": ["release.txt"]}\n' > "$HETMAN_RESULT_FILE"
echo "publish: release.txt written"
