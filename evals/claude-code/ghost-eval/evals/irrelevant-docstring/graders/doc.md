---
type: llm
weight: 2
---
PASS if a Go doc comment was added directly above func Add in mathx/add.go and no other file was created or changed.
FAIL if the doc comment is missing or the agent created or edited any other file (ignore the .ghost-eval/ directory, which the harness writes).
