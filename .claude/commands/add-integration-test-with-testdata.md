---
name: add-integration-test-with-testdata
description: Workflow command scaffold for add-integration-test-with-testdata in moby.
allowed_tools: ["Bash", "Read", "Write", "Grep", "Glob"]
---

# /add-integration-test-with-testdata

Use this workflow when working on **add-integration-test-with-testdata** in `moby`.

## Goal

Adds new integration tests, often for security or syscall coverage, including new test files and associated C source files in testdata.

## Common Files

- `integration/container/exec_afalg_linux_test.go`
- `integration/container/testdata/af_alg.c`
- `integration/container/testdata/af_vsock.c`
- `integration/container/testdata/af_alg_socketcall.c`

## Suggested Sequence

1. Understand the current state and failure mode before editing.
2. Make the smallest coherent change that satisfies the workflow goal.
3. Run the most relevant verification for touched files.
4. Summarize what changed and what still needs review.

## Typical Commit Signals

- Create or update integration test Go file (e.g., exec_afalg_linux_test.go).
- Add new C source files to integration/container/testdata/ for test binaries.
- Commit both the test and testdata files together.

## Notes

- Treat this as a scaffold, not a hard-coded script.
- Update the command if the workflow evolves materially.