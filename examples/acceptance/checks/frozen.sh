#!/bin/sh
# Blocks when the acceptance script or a fixture differs from what was
# accepted, and names the files that changed.
. lib/task.sh
if [ ! -f "$stored" ]; then
  echo "no fingerprint in $stored: $script was never accepted"
  exit 1
fi
now="$HETMAN_RUN_DIR/acceptance.now"
fingerprint > "$now" 2>/dev/null
if cmp -s "$stored" "$now"; then
  echo "$script and its fixtures are unchanged since they were accepted"
  exit 0
fi
changed=$(cat "$stored" "$now" | sort | uniq -u | awk '{ print $3 }' | sort -u)
echo "changed after it was accepted:" $changed
exit 1
