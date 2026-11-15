#!/bin/sh
# Tests one item on the shared test stand; the engine lets one test hold it
# at a time.
set -eu
id=$(sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' "$HETMAN_INPUT_FILE" | head -n 1)
echo "test: item $id on the stand"
test -s "$HETMAN_RUN_DIR/build.txt"
sleep "${FOREACH_SLEEP:-1}"
if [ "${FOREACH_FAIL:-}" = "$id" ]; then
  echo "test: item $id fails (FOREACH_FAIL)"
  exit 1
fi
echo "test: item $id passed"
