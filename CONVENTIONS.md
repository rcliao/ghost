# Project Conventions

These rules apply to all changes, including those made by automated agents.
A reviewer MUST flag violations as `needs_human`.

## Backwards Compatibility
- Existing stored data must remain readable after any change
- New validation rules must not reject data that was previously valid
- Schema changes require a migration path (ALTER TABLE, not recreate)
- CLI flag removals or renames are breaking — add the new flag alongside the old one

## Scope Control
- A single task should not touch more than 8 files (excluding tests)
- If a task requires changes across more than 3 packages, it should be split
- Do not add new dependencies (go.mod changes) without human approval
- Do not modify the database schema (CREATE TABLE, ALTER TABLE) unless the task explicitly calls for it

## Design Decisions That Require Human Approval
- New data models or tables
- Changes to the Store interface (adding/removing/changing method signatures)
- New CLI subcommands (adding a top-level command to root)
- Changes to the on-disk format or database schema
- Anything that affects how other tools consume agent-memory output (JSON structure changes)

## Code Quality
- All new code must have tests
- Use cmd.OutOrStdout() for all CLI output (for testability)
- Errors should include enough context to debug (what was attempted, what went wrong)
- No silent failures — if something is intentionally ignored, comment why
