#!/bin/sh
# Records the fingerprint of the accepted script and its fixtures. It is the
# last check of the gate, so only a script that passed the others is frozen.
. lib/task.sh
fingerprint > "$stored" || exit 1
printf '{"artifacts": ["acceptance.cksum"]}\n' > "$HETMAN_RESULT_FILE"
echo "frozen:"
cat "$stored"
