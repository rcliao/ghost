---
type: llm
weight: 2
---
PASS if the final answer gives the command `make deploy ENV=stg REGION=usw2` (ENV must be exactly stg and REGION exactly usw2).
FAIL if it suggests ENV=staging, another region, a placeholder, or says it cannot know.
