#!/bin/sh
# A required check: the draft step wrote note.md, and it is short.
f=note.md
if [ ! -s "$f" ]; then
  echo "note: $f is missing or empty"
  exit 1
fi
words=$(wc -w < "$f")
echo "note: $f has $words words"
[ "$words" -le 150 ]
