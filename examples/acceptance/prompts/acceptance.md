Task {{input.task}}: {{input.title}}

Write the acceptance script of this task to acceptance/{{input.task}}.sh, a POSIX
shell script. It checks the folder given as its argument (by default
$HETMAN_RUN_DIR, where the work lands), prints one line "PASS NAME" or "FAIL NAME"
per criterion and exits with 0 only when every criterion passes.

Also write two fixtures for it: acceptance/{{input.task}}/good, a folder on which
every criterion passes, and acceptance/{{input.task}}/bad, a folder on which every
criterion fails. Do not do the task itself.

Reply with a JSON object: "outcome" is "ok", "criteria" lists the criterion names.
{{rework}}
