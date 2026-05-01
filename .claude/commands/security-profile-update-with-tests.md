---
name: security-profile-update-with-tests
description: Workflow command scaffold for security-profile-update-with-tests in moby.
allowed_tools: ["Bash", "Read", "Write", "Grep", "Glob"]
---

# /security-profile-update-with-tests

Use this workflow when working on **security-profile-update-with-tests** in `moby`.

## Goal

Updates a security profile (seccomp) and adds or updates integration tests and testdata to verify the new restrictions.

## Common Files

- `go.mod`
- `go.sum`
- `vendor/github.com/moby/profiles/seccomp/default.json`
- `vendor/github.com/moby/profiles/seccomp/default_linux.go`
- `vendor/modules.txt`
- `integration/container/exec_afalg_linux_test.go`

## Suggested Sequence

1. Understand the current state and failure mode before editing.
2. Make the smallest coherent change that satisfies the workflow goal.
3. Run the most relevant verification for touched files.
4. Summarize what changed and what still needs review.

## Typical Commit Signals

- Update security profile files in vendor/ (e.g., default.json, default_linux.go).
- Update go.mod, go.sum, and vendor/modules.txt if a dependency version changes.
- Add or update integration test Go files.
- Add or update C source files in integration/container/testdata/.

## Notes

- Treat this as a scaffold, not a hard-coded script.
- Update the command if the workflow evolves materially.