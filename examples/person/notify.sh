#!/bin/sh
# Notification hook: Hetman runs it whenever a campaign starts waiting for
# a person and writes one line of JSON to its standard input (campaign,
# node, reason, message, questions, the files to look at and the command
# that answers or resumes). This one appends that line to a file.
mkdir -p .hetman
cat >> .hetman/notifications.jsonl
