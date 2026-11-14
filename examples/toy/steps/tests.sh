#!/bin/sh
# A required check: its failure blocks the gate.
echo "tests: check $HETMAN_CHECK_ID of gate $HETMAN_NODE_ID"
if [ "${TOY_TESTS_FAIL:-}" = 1 ]; then
  echo "tests: 1 failing test"
  exit 1
fi
grep -q HELLO "$HETMAN_RUN_DIR/build.txt"
