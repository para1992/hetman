#!/bin/sh
# Notification hook: Hetman runs it whenever a campaign starts waiting for
# a person and writes one line of JSON to its standard input. For a
# campaign the dispatcher started, the line names the task under "task"
# (id and kind). This one appends the line to a file.
mkdir -p .hetman
cat >> .hetman/notifications.jsonl
