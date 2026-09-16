package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIContainingBooleanExpression(t *testing.T) {
	for _, test := range []struct {
		name, body string
		operators  int
	}{
		{"comparison", "return (a && b && c) == (d && e && f)", 4},
		{"negation", "return !((a && b && c) == (d || e || f))", 4},
		{"separate expressions", "x := a && b && c; y := d && e && f; return x == y", 2},
		{"callback", "return (a && b) == func() bool { return c && d && e && f }()", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := "package example\nfunc f(a,b,c,d,e,f bool) bool { " + test.body + " }\n"
			if err := os.WriteFile(filepath.Join(dir, "example.go"), []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := run([]string{"-check", "-json", dir}, &out)
			if (err != nil) != (test.operators > defaultLocalLimit) {
				t.Fatalf("check: %v", err)
			}
			var got measurements
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Functions) == 0 || got.Functions[0].BooleanOps != test.operators {
				t.Fatalf("functions: %+v", got.Functions)
			}
		})
	}
}

func TestLayeredCLI(t *testing.T) {
	dir := t.TempDir()
	source := `package example
var table = map[string]int{
	"a": 1,
	"b": 2,
}
func nested(a, b, c, d, e bool) {
	if a && b &&
		(c || d || e) {
		for a {
			switch {
			case b:
				if c { work() }
			}
		}
	} else if b {
		work()
	} else if c {
		work()
	}
}
func callbacks() {
	if ready() {
		fn := func() { if ready() { work() } }
		fn()
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "example.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"-json", dir}, &output); err != nil {
		t.Fatal(err)
	}
	var got measurements
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Functions) != 3 || len(got.Files) != 1 || len(got.Packages) != 1 {
		t.Fatalf("measurements: %+v", got)
	}
	f := got.Functions[0]
	if f.Nesting != 4 || f.BooleanOps != 4 || f.CC != 11 {
		t.Fatalf("nested: %+v", f)
	}
	if got.Functions[1].Nesting != 1 || got.Functions[2].Nesting != 1 {
		t.Fatalf("callback depth not independent: %+v", got.Functions)
	}
	if got.Files[0].Decisions != 12 || got.Packages[0].Decisions != 12 {
		t.Fatalf("aggregates: %+v", got)
	}
	if got.Violations != (violations{CC: 1, Nesting: 1, BooleanOps: 1, Total: 1}) {
		t.Fatalf("violations: %+v", got.Violations)
	}
	output.Reset()
	if err := run([]string{"-check", "-at", "11", "-nesting", "4", "-boolean", "4", dir}, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"-check", "-at", "11", "-nesting", "4", "-boolean", "4", "-file-decisions", "11", dir}, &output); err == nil {
		t.Fatal("file ceiling was not enforced")
	}
	output.Reset()
	if err := run([]string{"-check", dir}, &output); err == nil {
		t.Fatal("function ceilings were not enforced")
	}
}

func TestExtractionPreservesDecisions(t *testing.T) {
	for _, source := range []string{
		"package example\nfunc f(a, b bool) { if a { work() }; if b { work() } }",
		"package example\nfunc f(a, b bool) { if a { work() }; g(b) }; func g(b bool) { if b { work() } }",
	} {
		got, err := measure("example.go", []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		if got.Decisions != 2 {
			t.Fatalf("decisions: %d", got.Decisions)
		}
	}
}

func TestSourceLOCIncludesDeclarations(t *testing.T) {
	got, err := measure("example.go", []byte("package example\n// comment\nvar table = map[string]int{\n\"a\": 1,\n\"b\": 2,\n}\n\nfunc f() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.LOC != 6 {
		t.Fatalf("source LOC = %d, want 6", got.LOC)
	}
	if got.Functions[0].LOC != 1 {
		t.Fatalf("function LOC = %d", got.Functions[0].LOC)
	}
}

func TestElseIfAndExpressions(t *testing.T) {
	for _, source := range []string{
		"package example\nfunc f(a,b,c bool) { if a { } else if b { } else if c { }; x := a && (b || c); _ = x }",
		"package example\nfunc f(a,b,c bool) { if a { } else if b { } else if c { }; x := a &&\n(b ||\nc); _ = x }",
	} {
		got, err := measure("example.go", []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		f := got.Functions[0]
		if f.Nesting != 1 || f.BooleanOps != 2 {
			t.Fatalf("metrics: %+v", f)
		}
	}
}

func TestPackageAggregation(t *testing.T) {
	dir := t.TempDir()
	for name, source := range map[string]string{
		"a.go":             "package example\nfunc a(x bool) { if x {} }",
		"b.go":             "package example\nfunc b(x bool) { if x {} }",
		"external_test.go": "package example_test\nfunc TestExternal() {}",
		"data.go":          "package example\nvar value = 1",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := run([]string{"-tests", "-json", dir}, &output); err != nil {
		t.Fatal(err)
	}
	var got measurements
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 2 || got.Packages[0].Decisions != 2 || got.Packages[1].Decisions != 0 {
		t.Fatalf("packages: %+v", got.Packages)
	}
	if len(got.Files) != 4 {
		t.Fatalf("files: %+v", got.Files)
	}
	total := 0
	for _, file := range got.Files {
		total += file.LOC
	}
	if got.LOC != total {
		t.Fatalf("LOC = %d, want %d", got.LOC, total)
	}
}
