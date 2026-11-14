#!/bin/sh
# A soft check: its failure is only a warning.
echo "lint: check $HETMAN_CHECK_ID of gate $HETMAN_NODE_ID"
if [ "${TOY_LINT_FAIL:-}" = 1 ]; then
  echo "lint: style problems found"
  exit 1
fi
