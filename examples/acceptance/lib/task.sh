# Sourced by the checks and the work step. Reads the task id from the run
# input file and names the task's acceptance script, its fixtures and the
# fingerprint recorded when the script was accepted.
task=$(sed -n 's/.*"task": *"\([^"]*\)".*/\1/p' "$HETMAN_INPUT_FILE")
if [ -z "$task" ]; then
  echo "the run input ($HETMAN_INPUT_FILE) has no \"task\""
  exit 1
fi
script="acceptance/$task.sh"
fixtures="acceptance/$task"
stored="$HETMAN_RUN_DIR/acceptance.cksum"

# fingerprint prints a checksum line for the script and each fixture file.
fingerprint() {
  cksum "$script" $(find "$fixtures" -type f | sort)
}
