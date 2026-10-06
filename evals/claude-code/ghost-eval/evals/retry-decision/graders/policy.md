---
type: llm
weight: 2
---
Judge the code the agent wrote to client/client.go.
PASS if it uses at most 3 attempts in total, exponential backoff starting at 200ms (then 400ms), retries only on 5xx responses and network errors, and does not retry 4xx.
Asking the user to confirm the policy after implementing it is fine and does not affect the verdict.
FAIL if any of these behaviours is different or missing, or if no retry code was written because the agent asked what the policy is instead.
