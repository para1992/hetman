#!/bin/sh
# Builds one item: writes what the test step looks for.
set -eu
id=$(sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' "$HETMAN_INPUT_FILE" | head -n 1)
echo "build: item $id, campaign $HETMAN_RUN_ID"
sleep "${FOREACH_SLEEP:-1}"
echo "built $id" > "$HETMAN_RUN_DIR/build.txt"
