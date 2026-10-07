---
type: llm
weight: 2
---
PASS if the answer tells the user to run `make migrate` (with ENV=stg) before the deploy, and gives the deploy command `make deploy ENV=stg REGION=usw2`.
FAIL if migrations are missing, come after the deploy, or the deploy command has a different ENV or REGION.
