#!/bin/sh
# A collector, as a scheduler or a script that watches a code review system
# would run it: it adds one task per finding or remark. The key makes a
# repeated report of the same thing a no-op, so the third task is not added.
# HETMAN names the hetman program (default: hetman on the path).
set -eu
cd "$(dirname "$0")"
hetman=${HETMAN:-hetman}

"$hetman" task add -kind machine-finding -key lint:3f2a91c:notes.txt -input - <<'JSON'
{"commit": "3f2a91c", "file": "notes.txt", "finding": "trailing whitespace"}
JSON

"$hetman" task add -kind reviewer-remark -key review:3f2a91c:17 -input - <<'JSON'
{"commit": "3f2a91c", "file": "notes.txt", "remark": "Some lines end in spaces."}
JSON

"$hetman" task add -kind machine-finding -key lint:3f2a91c:notes.txt -input - <<'JSON'
{"commit": "3f2a91c", "file": "notes.txt", "finding": "trailing whitespace"}
JSON
