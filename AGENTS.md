# AGENTS.md

Rules for anyone (human or model) writing Go in this repository. They are
not suggestions: code that breaks them does not pass review.

`sqlgofmt` formats SQL inside raw string literals of Go source files,
powered by GoSQLX. Formatting must be idempotent and byte-exact outside the
literals — that invariant outranks every style rule below.

## Conventions

- Layout: `cmd/sqlgofmt/main.go` (CLI), `internal/processor/` (engine:
  `processor.go`, `layout.go`). Nothing else at the top level.
- Plain errors: `fmt.Errorf` / `errors.New`, no custom error types. CLI
  errors print as `fmt.Fprintln(os.Stderr, "sqlgofmt:", err)` and continue;
  the exit code is decided at the end, not per file.
- Tests are the product here: table tests with `t.Run` and named cases,
  plus three guards that must stay green — the idempotency corpus
  (`TestCorpusRoundTrip` over `testdata/corpus/`), the fuzz test, and the
  pg_query oracle test. Any new formatting path adds a corpus fixture.

## Go 1.27 idioms

Use the current language, not the pre-1.22 spellings. `golangci-lint` with
`modernize` enabled is the reviewer of record:

- `slices.Sort` / `slices.SortFunc` instead of `sort.Strings` /
  `sort.Slice` (`cmd/sqlgofmt/main.go`, `internal/processor/layout.go`).
- `strings.SplitSeq` instead of `strings.Split` when the slice is only
  ranged, never indexed (`processor.go:346` and test loops); indexed or
  stateful scanners keep `Split`/index loops.
- `bytes.Clone(src)` / `slices.Clone` instead of
  `make`+`copy` pairs (`processor.go`, `layout.go`).
- `for i := range n` instead of `for i := 0; i < n; i++` — but only for the
  simple loops: byte scanners that mutate `i` or use `goto` stay as-is.

## UUID

No uuid usage in this repo. If one is ever needed, use the stdlib `uuid`
package (Go 1.27+), never `github.com/google/uuid`.

## Checklist before handing over

- [ ] `gofmt -l .` empty, `go vet ./...` clean
- [ ] `go test ./...` clean — corpus, fuzz seed corpus and oracle included
- [ ] `golangci-lint run` clean with `modernize` enabled (add it to the
      config before enabling the linter, don't silence its findings)
- [ ] formatting is still idempotent: run `sqlgofmt` over
      `testdata/corpus/` twice
