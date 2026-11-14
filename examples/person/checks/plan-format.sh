#!/bin/sh
# Passes when plan.md exists and has a title line starting with "# ".
grep -q '^# ' plan.md
