#!/bin/sh
# Applies one fix per attempt: removes the trailing whitespace of the first
# line that has some, in the campaign's copy of the file the task names.
# The gate after it sends the flow back here until nothing is left.
set -eu
file=$(sed -n 's/^  "file": "\(.*\)",\{0,1\}$/\1/p' "$HETMAN_INPUT_FILE")
work="$HETMAN_RUN_DIR/$file"
if [ ! -f "$work" ]; then
  cp "sample/$file" "$work"
fi
if [ "$HETMAN_ATTEMPT" -gt 5 ]; then
  echo "still not fixed after 5 attempts"
  exit 1
fi
awk 'fixed == 0 && /[ \t]+$/ { sub(/[ \t]+$/, ""); fixed = NR } { print } END { if (fixed) print "fixed line " fixed > "/dev/stderr" }' "$work" > "$work.new"
mv "$work.new" "$work"
