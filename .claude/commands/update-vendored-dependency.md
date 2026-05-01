---
name: update-vendored-dependency
description: Workflow command scaffold for update-vendored-dependency in moby.
allowed_tools: ["Bash", "Read", "Write", "Grep", "Glob"]
---

# /update-vendored-dependency

Use this workflow when working on **update-vendored-dependency** in `moby`.

## Goal

Updates a vendored dependency (here, moby/profiles/seccomp) to a new version, including updating go.mod, go.sum, vendor files, and modules.txt.

## Common Files

- `go.mod`
- `go.sum`
- `vendor/github.com/moby/profiles/seccomp/default.json`
- `vendor/github.com/moby/profiles/seccomp/default_linux.go`
- `vendor/modules.txt`

## Suggested Sequence

1. Understand the current state and failure mode before editing.
2. Make the smallest coherent change that satisfies the workflow goal.
3. Run the most relevant verification for touched files.
4. Summarize what changed and what still needs review.

## Typical Commit Signals

- Update go.mod to specify the new dependency version.
- Update go.sum to reflect new dependency checksums.
- Update files under vendor/ for the dependency (e.g., JSON configs, Go files).
- Update vendor/modules.txt.

## Notes

- Treat this as a scaffold, not a hard-coded script.
- Update the command if the workflow evolves materially.