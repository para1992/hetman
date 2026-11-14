#!/bin/sh
# Checks that the build artifact exists and is not empty.
set -eu
echo "test: attempt $HETMAN_ATTEMPT"
test -s "$HETMAN_RUN_DIR/build.txt"
echo "test: build.txt is present"
