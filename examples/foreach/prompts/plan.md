Split this work into items that can be built and tested one by one: {{input.goal}}

Reply with a JSON object: "outcome" is "ok" and "items" lists the items. Each
item has an "id" (letters, digits, '_', '-' and '.'), a "title", and "needs":
the ids of the items that must be done before it can start. Leave "needs" out
for an item that can start at once.
{{rework}}
