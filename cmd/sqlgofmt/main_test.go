package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajitpratap0/GoSQLX/pkg/sql/keywords"
)

func TestResolveDialect(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want keywords.SQLDialect
		err  bool
	}{
		{name: "postgres", in: "postgres", want: keywords.DialectPostgreSQL},
		{name: "postgresql", in: "postgresql", want: keywords.DialectPostgreSQL},
		{name: "mysql", in: "mysql", want: keywords.DialectMySQL},
		{name: "sqlserver", in: "sqlserver", want: keywords.DialectSQLServer},
		{name: "mssql", in: "mssql", want: keywords.DialectSQLServer},
		{name: "oracle", in: "oracle", want: keywords.DialectOracle},
		{name: "clickhouse", in: "clickhouse", want: keywords.DialectClickHouse},
		{name: "case insensitive", in: "MySQL", want: keywords.DialectMySQL},
		{name: "unknown", in: "bigtable", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDialect(tt.in)
			if tt.err {
				if err == nil {
					t.Fatalf("expected error for %q, got dialect %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveDialect(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDiffWriteExclusive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.go")
	if err := os.WriteFile(file, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := run([]string{"-d", "-w", file})
	if err == nil {
		t.Fatal("expected error for -d -w, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListMode(t *testing.T) {
	dir := t.TempDir()
	// unformatted.go contains SQL that the formatter would change.
	unformatted := "package x\n\nfunc q(db *sql.DB) error {\n\t_, err := db.Exec(`\nSELECT a FROM t WHERE x = $1 AND y = 2`)\n\treturn err\n}\n"
	formatted := "package x\n\nfunc f() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(dir, "unformatted.go"), []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "formatted.go"), []byte(formatted), 0o644); err != nil {
		t.Fatal(err)
	}

	// -l must report only the file whose formatting differs.
	changed, listMode, err := run([]string{"-l", dir})
	if err != nil {
		t.Fatalf("run -l: %v", err)
	}
	if changed != 1 {
		t.Fatalf("run -l changed = %d, want 1", changed)
	}
	if !listMode {
		t.Fatal("run -l must return listMode=true")
	}

	// After -w, a second -l must report zero.
	if _, _, err := run([]string{"-w", dir}); err != nil {
		t.Fatalf("run -w: %v", err)
	}
	changed, listMode, err = run([]string{"-l", dir})
	if err != nil {
		t.Fatalf("run -l after -w: %v", err)
	}
	if changed != 0 {
		t.Fatalf("run -l after -w changed = %d, want 0", changed)
	}
	if !listMode {
		t.Fatal("run -l must return listMode=true")
	}

	// A file that fails to parse must not abort the -l run: processing
	// continues and the error surfaces (main turns it into exit 1).
	broken := filepath.Join(dir, "broken.go")
	if err := os.WriteFile(broken, []byte("package x\nfunc {{{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unformatted.go"), []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, listMode, err = run([]string{"-l", dir})
	if err == nil {
		t.Fatal("expected error for broken file, got nil")
	}
	if changed != 1 {
		t.Fatalf("run -l with broken file changed = %d, want 1 (other files still processed)", changed)
	}
}

func TestCollectFilesDotDotDot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.go", "sub/b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := collectFiles([]string{filepath.Join(dir, "...")})
	if err != nil {
		t.Fatalf("collectFiles with /... suffix: %v", err)
	}
	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "sub", "b.go")}
	if len(files) != len(want) {
		t.Fatalf("collectFiles = %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("collectFiles[%d] = %s, want %s", i, files[i], want[i])
		}
	}
	// Bare "..." walks the current directory.
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	files, err = collectFiles([]string{"..."})
	if err != nil {
		t.Fatalf("collectFiles with bare ...: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("bare ... collected %v, want 2 .go files", files)
	}
}
