#!/bin/sh
# Turns the source into a build artifact. TOY_SLEEP makes it slow.
set -eu
echo "build: attempt $HETMAN_ATTEMPT"
sleep "${TOY_SLEEP:-1}"
tr 'a-z' 'A-Z' < "$HETMAN_RUN_DIR/source.txt" > "$HETMAN_RUN_DIR/build.txt"
echo "build: wrote build.txt"
