```markdown
# moby Development Patterns

> Auto-generated skill from repository analysis

## Overview

This skill teaches you how to contribute effectively to the [moby](https://github.com/moby/moby) codebase, a large-scale Go project for containerization. You'll learn the project's coding conventions, how to manage vendored dependencies, update security profiles, and add integration tests, including workflows and commands for common tasks.

## Coding Conventions

- **File Naming:**  
  Use `snake_case` for file names.  
  _Example:_  
  ```
  exec_afalg_linux_test.go
  af_alg_socketcall.c
  ```

- **Import Style:**  
  Use **relative imports** within the project.  
  _Example:_  
  ```go
  import (
      "github.com/moby/moby/integration/container"
      "github.com/moby/moby/vendor/github.com/moby/profiles/seccomp"
  )
  ```

- **Export Style:**  
  Use **named exports** for functions, types, and variables.  
  _Example:_  
  ```go
  // Exported function
  func RunContainerTest() error {
      // ...
  }
  ```

## Workflows

### update-vendored-dependency
**Trigger:** When you need to update a vendored Go dependency to a new version  
**Command:** `/update-vendor`

1. Update `go.mod` to specify the new dependency version.
2. Update `go.sum` to reflect new dependency checksums.
3. Update files under `vendor/` for the dependency (e.g., JSON configs, Go files).
4. Update `vendor/modules.txt`.

_Example:_
```sh
go get github.com/moby/profiles/seccomp@vX.Y.Z
go mod tidy
cp path/to/new/default.json vendor/github.com/moby/profiles/seccomp/default.json
cp path/to/new/default_linux.go vendor/github.com/moby/profiles/seccomp/default_linux.go
go mod vendor
```

### add-integration-test-with-testdata
**Trigger:** When adding new integration tests that require custom binaries or syscall coverage  
**Command:** `/add-integration-test`

1. Create or update an integration test Go file (e.g., `exec_afalg_linux_test.go`).
2. Add new C source files to `integration/container/testdata/` for test binaries.
3. Commit both the test and testdata files together.

_Example:_
```go
// integration/container/exec_afalg_linux_test.go
func TestAfAlgSocket(t *testing.T) {
    // Test logic here
}
```
```c
// integration/container/testdata/af_alg.c
#include <stdio.h>
int main() {
    // C test binary logic
    return 0;
}
```

### security-profile-update-with-tests
**Trigger:** When updating a security profile (seccomp) and verifying with integration tests  
**Command:** `/update-seccomp-profile`

1. Update security profile files in `vendor/` (e.g., `default.json`, `default_linux.go`).
2. Update `go.mod`, `go.sum`, and `vendor/modules.txt` if a dependency version changes.
3. Add or update integration test Go files.
4. Add or update C source files in `integration/container/testdata/`.

_Example:_
```sh
# Update seccomp profile
cp new_default.json vendor/github.com/moby/profiles/seccomp/default.json
# Update Go test
vim integration/container/exec_afalg_linux_test.go
# Add new testdata
cp af_alg_socketcall.c integration/container/testdata/
```

## Testing Patterns

- **Test Framework:**  
  The specific test framework is not explicitly stated, but Go's standard testing package is implied.

- **Test File Pattern:**  
  Test files follow the pattern `*_test.go` for Go tests and may include C source files in `testdata/`.

  _Example:_
  ```
  integration/container/exec_afalg_linux_test.go
  integration/container/testdata/af_alg.c
  ```

- **Integration Tests:**  
  Integration tests may require custom binaries, placed as C source files under `integration/container/testdata/`.

## Commands

| Command                | Purpose                                                        |
|------------------------|----------------------------------------------------------------|
| /update-vendor         | Update a vendored Go dependency to a new version               |
| /add-integration-test  | Add a new integration test with associated testdata            |
| /update-seccomp-profile| Update seccomp profile and verify with integration tests       |
```
