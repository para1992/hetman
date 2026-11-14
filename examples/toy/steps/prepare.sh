#!/bin/sh
# Writes the "source" the later steps work on.
set -eu
echo "prepare: attempt $HETMAN_ATTEMPT of campaign $HETMAN_RUN_ID"
echo "hello from campaign $HETMAN_RUN_ID" > "$HETMAN_RUN_DIR/source.txt"
