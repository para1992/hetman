#!/bin/sh
# The generic acceptance check: runs the acceptance script of whatever task
# the run input names.
. lib/task.sh
exec sh "$script"
