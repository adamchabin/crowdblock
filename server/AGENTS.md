# AGENTS.md

Instructions for AI agents writing and modifying Go code in this repository.

## Core principles

- Make the smallest change that solves the task. Do not rewrite files, rename symbols, or reformat code you were not asked to touch.
- Follow existing patterns in the codebase over personal preference. Read neighbouring code before writing new code.
- Do not guess. If requirements, APIs, or behaviour are unclear, ask or state the assumption explicitly.
- Never invent packages, functions, or flags. Verify they exist (`go doc`, source, `go.mod`) before using them.
- Prefer boring, readable code over clever code.
- Always use English when writing code, but communicate with me in Polish.

## Workflow

1. Read the relevant code and tests first.
2. Propose a short plan for non-trivial changes before implementing.
3. Implement the change in small, reviewable steps.
4. Run the checks listed below and fix any failures you introduced.
5. Summarize what changed and why. Keep it brief.

## Commands

Run these before declaring a task done:

```sh
gofmt -l .            # must print nothing
go vet ./...
go build ./...
go test ./... -race -count=1
golangci-lint run     # if .golangci.yml exists
```

- Use `make test` / `make lint` instead if a Makefile defines them.
- Do not disable linters, skip tests, or add `//nolint` to make checks pass without a written justification.

## Project layout

- `cmd/<app>/` — entrypoints only; keep `main` thin (wiring, config, signals).
- `internal/` — private application code. Default location for new packages.
- `pkg/` — only for code intentionally exposed to other modules.
- Package names: short, lowercase, singular, no underscores. No `util`, `common`, or `helpers` packages.
- Avoid import cycles by depending on small interfaces, not concrete packages.

## Code style

- Code must be `gofmt`-clean and pass `goimports` ordering (stdlib, third-party, local).
- Follow Effective Go and the Go Code Review Comments.
- Exported identifiers need doc comments starting with the identifier name.
- Keep functions short and focused. Prefer early returns over deep nesting.
- Avoid package-level mutable state and `init()` functions.
- Accept interfaces, return concrete types. Define interfaces at the consumer side, and keep them small.
- Use `any` instead of `interface{}`. Use generics only when they remove real duplication.
- Use the standard library first (`log/slog`, `net/http`, `encoding/json`, `errors`, `slices`, `maps`) before adding a dependency.
- All code comments must be written in English.

## Error handling

- Never ignore errors. If you intentionally discard one, comment why.
- Wrap errors with context: `fmt.Errorf("load config %q: %w", path, err)`.
- Use `errors.Is` / `errors.As` for checks; never compare error strings.
- Define sentinel errors or custom error types only when callers need to branch on them.
- Do not `panic` in library or request-handling code. Reserve it for truly unrecoverable programmer errors.
- Log or return an error, not both.

## Concurrency

- Pass `context.Context` as the first parameter of any function that does I/O or can block. Never store it in a struct.
- Every goroutine must have a clear owner and a defined way to stop. No fire-and-forget goroutines.
- Prefer `errgroup.Group` for fan-out with error propagation.
- Protect shared state with `sync.Mutex` or channels; document which fields a mutex guards.
- Always run tests with `-race`.
- Close channels only from the sender side.

## Testing

- Write table-driven tests with `t.Run` subtests.
- Use `t.Helper()` in helpers, `t.Cleanup()` for teardown, `t.TempDir()` for files, and `t.Parallel()` where safe.
- Test behaviour through the public API; avoid testing private implementation details.
- Prefer hand-written fakes over heavy mocking frameworks.
- Use `testify` only if it is already a dependency; otherwise use the standard library.
- Every bug fix must come with a regression test.
- Do not rely on `time.Sleep` for synchronization; use channels, `sync.WaitGroup`, or fake clocks.

## Dependencies

- Do not add or upgrade dependencies unless the task requires it. Explain why when you do.
- After changing imports, run `go mod tidy` and include `go.mod` / `go.sum` changes.
- Never edit `go.sum` manually.
- Do not vendor or replace modules without explicit approval.

## Logging and observability

- Use structured logging (`log/slog`) with key-value pairs; no `fmt.Println` or `log.Printf` in production code.
- Never log secrets, tokens, passwords, or full request bodies containing personal data.
- Propagate trace/request IDs through `context.Context`.

## Security

- Never hardcode credentials, tokens, or keys. Read configuration from environment variables or the existing config mechanism.
- Validate and sanitize all external input.
- Use `crypto/rand`, not `math/rand`, for anything security-sensitive.
- Use parameterized queries; never build SQL with string concatenation.
- Set timeouts on all HTTP clients, servers, and database calls. Do not use `http.DefaultClient` in production code.

## Performance

- Do not optimize without a benchmark or profile. Write a `Benchmark*` function first when performance matters.
- Preallocate slices and maps when the size is known.
- Avoid unnecessary allocations in hot paths, but never at the cost of readability without evidence.

## Things you must not do

- Do not modify generated files (`*.pb.go`, `*_gen.go`, mocks). Regenerate them using the documented command instead.
- Do not change public APIs or exported signatures without calling it out explicitly.
- Do not commit, push, or alter CI configuration unless asked.
- Do not delete tests or loosen assertions to get a green build.
- Do not leave `TODO`s without an owner or issue reference.
- Do not include unrelated refactors in the same change.

## Commit and PR conventions

- Use Conventional Commits: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`.
- Keep commits atomic; one logical change per commit.
- PR descriptions state what changed, why, and how it was tested.

## When unsure

Stop and ask. A short clarifying question is cheaper than a large incorrect change.
