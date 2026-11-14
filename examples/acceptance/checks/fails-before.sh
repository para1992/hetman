#!/bin/sh
# The acceptance script must fail on the project before the work is done.
. lib/task.sh
if sh "$script"; then
  echo "$script passes before the work is done, so it cannot tell done from not done"
  exit 1
fi
echo "$script fails before the work, as it must"
