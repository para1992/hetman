#!/bin/sh
# Passes when the campaign's copy of the file has no trailing whitespace.
set -eu
file=$(sed -n 's/^  "file": "\(.*\)",\{0,1\}$/\1/p' "$HETMAN_INPUT_FILE")
if grep -n '[[:space:]]$' "$HETMAN_RUN_DIR/$file"; then
  echo "trailing whitespace is left on the lines above"
  exit 1
fi
echo "no trailing whitespace"
