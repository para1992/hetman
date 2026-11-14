#!/bin/sh
# Proves the acceptance script valid: run on the bad fixture every criterion
# must FAIL, run on the good fixture every criterion must PASS. Writes one
# PASS or FAIL line per criterion to self-test.log, listed as an artifact.
. lib/task.sh
log="$HETMAN_RUN_DIR/self-test.log"
sh "$script" "$fixtures/bad" > "$HETMAN_RUN_DIR/self-test.bad" 2>&1
bad_exit=$?
sh "$script" "$fixtures/good" > "$HETMAN_RUN_DIR/self-test.good" 2>&1
good_exit=$?
printf '{"artifacts": ["self-test.log"]}\n' > "$HETMAN_RESULT_FILE"

verdict() { awk -v n="$1" '$2 == n { print $1 }' "$2"; }
names=$(sed -n -e 's/^PASS //p' -e 's/^FAIL //p' "$HETMAN_RUN_DIR/self-test.bad" "$HETMAN_RUN_DIR/self-test.good" | sort -u)
status=0
: > "$log"
for name in $names; do
  on_bad=$(verdict "$name" "$HETMAN_RUN_DIR/self-test.bad")
  on_good=$(verdict "$name" "$HETMAN_RUN_DIR/self-test.good")
  if [ "$on_bad" = FAIL ] && [ "$on_good" = PASS ]; then
    echo "PASS $name: fails on the bad fixture, passes on the good one" >> "$log"
  else
    echo "FAIL $name: on the bad fixture ${on_bad:-missing}, on the good one ${on_good:-missing}" >> "$log"
    status=1
  fi
done
if [ -z "$names" ]; then
  echo "FAIL the script reports no criteria" >> "$log"
  status=1
fi
if [ "$bad_exit" -eq 0 ] || [ "$good_exit" -ne 0 ]; then
  echo "FAIL exit codes: $bad_exit on the bad fixture (must not be 0), $good_exit on the good one (must be 0)" >> "$log"
  status=1
fi
cat "$log"
exit $status
