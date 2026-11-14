#!/bin/sh
# Does the work of the task. GREETING changes what it writes, to see the
# acceptance check block; EDIT_ACCEPTANCE makes it rewrite the accepted
# script, to see the frozen check block.
echo "criteria to meet (the accepted output of write_acceptance):"
cat "$HETMAN_RUN_DIR/outputs/write_acceptance.json"
printf '%s\n' "${GREETING:-hello}" > "$HETMAN_RUN_DIR/greeting.txt"
if [ -n "${EDIT_ACCEPTANCE:-}" ]; then
  . lib/task.sh
  printf 'echo PASS has-greeting\necho PASS says-hello\n' > "$script"
  echo "rewrote $script"
fi
