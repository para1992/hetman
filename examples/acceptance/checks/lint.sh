#!/bin/sh
# The greeting has no trailing spaces.
if grep -q ' $' "$HETMAN_RUN_DIR/greeting.txt"; then
  echo "greeting.txt has trailing spaces"
  exit 1
fi
