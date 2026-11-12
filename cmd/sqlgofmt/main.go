// Command sqlgofmt formats SQL inside raw string literals (backticks) of
// Go source files using GoSQLX.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ajitpratap0/GoSQLX/pkg/sql/keywords"
	"github.com/pmezard/go-difflib/difflib"

	"github.com/RevikaLabs/sqlgofmt/internal/processor"
)

var dialectMap = map[string]keywords.SQLDialect{
	"postgres":   keywords.DialectPostgreSQL,
	"postgresql": keywords.DialectPostgreSQL,
	"mysql":      keywords.DialectMySQL,
	"sqlserver":  keywords.DialectSQLServer,
	"mssql":      keywords.DialectSQLServer,
	"oracle":     keywords.DialectOracle,
	"clickhouse": keywords.DialectClickHouse,
}

// resolveDialect maps a CLI dialect name to a keywords.SQLDialect.
func resolveDialect(name string) (keywords.SQLDialect, error) {
	d, ok := dialectMap[strings.ToLower(name)]
	if !ok {
		names := make([]string, 0, len(dialectMap))
		for n := range dialectMap {
			names = append(names, n)
		}
		sort.Strings(names)
		return "", fmt.Errorf("unsupported dialect %q (supported: %s)", name, strings.Join(names, ", "))
	}
	return d, nil
}

const usageText = `sqlgofmt — format SQL inside Go raw string literals (backticks).

It finds SQL queries in const/var literals and db calls, re-indents them
to a house style (left-aligned keywords, tab indent, trailing commas,
AND/OR at line start, lists broken at 80 columns / >4 items) and never
changes tokens, identifiers or comments. DDL is left untouched.
Style synthesized from sqlstyle.guide, Mozilla's data-docs guide and
dbt conventions. Details: github.com/RevikaLabs/sqlgofmt

Usage:
  sqlgofmt [flags] <path>...

  <path> is a .go file or a directory (walked recursively; vendor,
  testdata and dot-directories are skipped).

Modes:
  (default)     print the formatted file to stdout
  -d            print unified diffs instead of formatted output
  -l            print names of files whose formatting differs;
                exits 1 if any (CI gate, like gofmt -l)
  -w            rewrite files in place

Flags:
  -d                 print unified diffs instead of formatted output
  -l                 list files whose formatting differs (exit 1 if any)
  -w                 write result to (source) file instead of stdout
  -dialect <name>    SQL dialect for the validation gate
                     (default "postgres"; postgres|mysql|sqlserver|oracle|clickhouse)
  -debug             print reasons for skipped literals to stderr
  -h                 show this help

  -d and -w are mutually exclusive.

Examples:
  sqlgofmt -d internal/store             preview diffs for a package dir
  sqlgofmt -l ./...                      CI check: exit 1 if anything is unformatted
  sqlgofmt -w internal/store/queries.go  format a file in place
  sqlgofmt -d -dialect mysql ./db        validate and preview with a different dialect

Skipped (left untouched): DDL statements, single-line literals that are
not valid SQL, struct tags, concatenated string fragments and literals
containing "::::" (sqlx escaping). Format verbs (%s, %d, ...) are NOT
skipped: they are masked for validation and emitted byte-exact. Use a
//sqlgofmt:ignore comment on a literal to opt it out.
`

func main() {
	changed, list, err := run(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h/-help: usage was already printed, exit cleanly.
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "sqlgofmt:", err)
		os.Exit(1)
	}
	// -l is a CI gate: exit 1 when any file's formatting differs.
	if list && changed > 0 {
		os.Exit(1)
	}
}

// run formats the given paths. It returns the number of files whose
// formatting differed and whether -l mode was requested.
func run(args []string) (changed int, listMode bool, err error) {
	fs_ := flag.NewFlagSet("sqlgofmt", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	// Hand-rolled help: flag.PrintDefaults renders poorly.
	// WriteString, not Fprint: usageText contains % verbs (SQL examples).
	fs_.Usage = func() {
		_, _ = os.Stderr.WriteString(usageText)
	}
	write := fs_.Bool("w", false, "write result to (source) file instead of stdout")
	list := fs_.Bool("l", false, "list files whose formatting differs (exit code 1 if any)")
	diff := fs_.Bool("d", false, "print diffs instead of formatted output")
	dialect := fs_.String("dialect", "postgres", "SQL dialect: postgres|mysql|sqlserver|oracle|clickhouse")
	debug := fs_.Bool("debug", false, "print reasons for skipped literals to stderr")
	if err := fs_.Parse(args); err != nil {
		// -h/-help reach here as flag.ErrHelp after usage was printed.
		return 0, false, err
	}

	// Like gofmt: -d and -w are mutually exclusive.
	if *diff && *write {
		return 0, false, errors.New("-d and -w are mutually exclusive")
	}
	listMode = *list

	d, err := resolveDialect(*dialect)
	if err != nil {
		return 0, false, err
	}

	paths := fs_.Args()
	if len(paths) == 0 {
		fs_.Usage()
		return 0, false, errors.New("no paths given")
	}

	files, err := collectFiles(paths)
	if err != nil {
		return 0, false, err
	}

	opt := processor.Options{Dialect: d, Debug: *debug}
	// gofmt-like behavior: a per-file failure is reported to stderr and
	// processing continues; the run exits 1 at the end.
	var firstErr error
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sqlgofmt:", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out, fileChanged, err := processor.Process(f, src, opt)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sqlgofmt:", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		switch {
		case *diff && fileChanged:
			if err := printDiff(f, src, out); err != nil {
				return changed, listMode, err
			}
			changed++
		case *write && fileChanged:
			mode := fs.FileMode(0o644)
			if info, err := os.Stat(f); err == nil {
				mode = info.Mode()
			}
			if err := os.WriteFile(f, out, mode); err != nil {
				fmt.Fprintln(os.Stderr, "sqlgofmt:", err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			changed++
		}
		if *list && fileChanged {
			fmt.Println(f)
			changed++
		}
		if !*list && !*diff && !*write {
			// Print the formatted file (identical to source when unchanged).
			if _, err := os.Stdout.Write(out); err != nil {
				return changed, listMode, err
			}
		}
	}
	if firstErr != nil {
		return changed, listMode, firstErr
	}
	return changed, listMode, nil
}

// collectFiles expands paths into a list of .go files. Directories are
// walked recursively, skipping vendor, .git, testdata and dot-dirs.
// A path ending in "/..." walks that directory (Go pattern style).
func collectFiles(paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		// "dir/..." walks dir; bare "..." walks the current directory.
		if p == "..." {
			p = "."
		} else if trimmed, ok := strings.CutSuffix(p, "/..."); ok {
			p = trimmed
			if p == "" {
				p = "."
			}
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !strings.HasSuffix(p, ".go") {
				return nil, fmt.Errorf("%s: not a .go file", p)
			}
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != p && (name == "vendor" || name == ".git" || name == "testdata" || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func printDiff(name string, src, out []byte) error {
	d := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(src)),
		B:        difflib.SplitLines(string(out)),
		FromFile: name,
		ToFile:   name,
		Context:  3,
	}
	buf, err := difflib.GetUnifiedDiffString(d)
	if err != nil {
		return err
	}
	_, err = os.Stdout.WriteString(buf)
	return err
}
