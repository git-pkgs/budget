package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
)

type function struct {
	File       string
	Line       int
	Name       string
	LOC        int
	CC         int
	Nesting    int
	BooleanOps int
}

type curve struct{ base, at, size, power float64 }

const (
	defaultCeiling    = 10
	defaultLOC        = 40
	defaultPower      = 0
	defaultTop        = 20
	tabWidth          = 4
	tabPadding        = 2
	defaultLocalLimit = 3
)

func (c curve) ceiling(loc int) float64 {
	return c.base + (c.at-c.base)*math.Pow(float64(loc)/c.size, c.power)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil && err != flag.ErrHelp {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("budget", flag.ContinueOnError)
	flags.SetOutput(out)
	c := curve{}
	flags.Float64Var(&c.base, "base", 1, "minimum ceiling")
	flags.Float64Var(&c.at, "at", defaultCeiling, "ceiling at reference LOC")
	flags.Float64Var(&c.size, "loc", defaultLOC, "reference LOC")
	flags.Float64Var(&c.power, "power", defaultPower, "exponent: 0 is fixed, 0 < p < 1 is sublinear")
	tests := flags.Bool("tests", false, "include test files")
	jsonOutput := flags.Bool("json", false, "emit raw function measurements as JSON")
	top := flags.Int("top", defaultTop, "number of violations to display")
	exclude := flags.String("exclude", "vendor,testdata,scripts", "directory names to skip; hidden directories are always skipped")
	l := limits{}
	flags.IntVar(&l.BooleanOps, "boolean", defaultLocalLimit, "maximum && and || operators in one expression")
	flags.IntVar(&l.Nesting, "nesting", defaultLocalLimit, "maximum nested control structures; else-if chains count as one level")
	flags.IntVar(&l.FileDecisions, "file-decisions", 0, "maximum sum of CC-1 per file; 0 reports without enforcing")
	check := flags.Bool("check", false, "return failure when a configured limit is exceeded")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validate(c, l, *top, flags.NArg()); err != nil {
		return err
	}
	root := "."
	if flags.NArg() == 1 {
		root = flags.Arg(0)
	}
	data, err := collect(root, *tests, exclusions(*exclude))
	if err != nil {
		return err
	}
	data.evaluate(c, l)
	if *jsonOutput {
		err = json.NewEncoder(out).Encode(data)
	} else {
		err = report(out, data, c, l, *top)
	}
	if err == nil && *check && data.Violations.Total > 0 {
		return fmt.Errorf("%d functions/files exceed configured limits", data.Violations.Total)
	}
	return err
}

func exclusions(value string) []string {
	var names []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func validate(c curve, l limits, top, directories int) error {
	if !c.valid() || top < 0 || directories > 1 {
		return fmt.Errorf("require base >= 1, at >= base, loc > 0, 0 <= power < 1, top >= 0, and at most one directory")
	}
	if min(l.BooleanOps, l.Nesting, l.FileDecisions) < 0 {
		return fmt.Errorf("boolean, nesting, and file-decisions must be nonnegative")
	}
	return nil
}

func (c curve) valid() bool {
	for _, v := range []float64{c.base, c.at, c.size, c.power} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	shape := c.base >= 1 && c.at >= c.base && c.size > 0
	exponent := c.power >= 0 && c.power < 1
	return shape && exponent
}

func collect(root string, tests bool, excluded []string) (measurements, error) {
	data := measurements{Functions: []function{}}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || slices.Contains(excluded, entry.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceFile(path, tests) {
			return nil
		}
		found, err := readMeasurement(root, path)
		if err != nil {
			return err
		}
		if found != nil {
			data.Functions = append(data.Functions, found.Functions...)
			data.Files = append(data.Files, found.aggregate)
		}
		return nil
	})
	data.aggregatePackages()
	return data, err
}

func readMeasurement(root, path string) (*fileMeasurement, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	if name == "." {
		name = filepath.Base(path)
	}
	return measure(filepath.ToSlash(name), source)
}

func sourceFile(path string, tests bool) bool {
	return strings.HasSuffix(path, ".go") && (tests || !strings.HasSuffix(path, "_test.go"))
}

func measure(path string, input any) (*fileMeasurement, error) {
	originalSet := token.NewFileSet()
	original, err := parser.ParseFile(originalSet, path, input, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if ast.IsGenerated(original) {
		return nil, nil
	}
	var formatted bytes.Buffer
	err = format.Node(&formatted, originalSet, original)
	if err != nil {
		return nil, err
	}
	source := formatted.Bytes()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return nil, err
	}
	nodes, originals := functionNodes(file), functionNodes(original)
	if len(nodes) != len(originals) {
		return nil, fmt.Errorf("%s: formatting changed function count from %d to %d", path, len(originals), len(nodes))
	}
	result := &fileMeasurement{aggregate: aggregate{Name: path, Package: filepath.Dir(path) + ":" + file.Name.Name, LOC: lines(fset, source, file, nil)}}
	for i, node := range nodes {
		name, err := functionName(fset, node)
		if err != nil {
			return nil, err
		}
		cc, nested := complexity(node)
		nesting, boolean := localComplexity(node)
		result.Functions = append(result.Functions, function{File: path, Line: originalSet.PositionFor(originals[i].Pos(), false).Line, Name: name, LOC: lines(fset, source, node, nested), CC: cc, Nesting: nesting, BooleanOps: boolean})
		result.Decisions += cc - 1
	}
	result.FunctionCount = len(result.Functions)
	return result, nil
}

func functionNodes(file *ast.File) (nodes []ast.Node) {
	for n := range ast.Preorder(file) {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				nodes = append(nodes, n)
			}
		case *ast.FuncLit:
			nodes = append(nodes, n)
		}
	}
	return nodes
}

func functionName(fset *token.FileSet, node ast.Node) (string, error) {
	decl, ok := node.(*ast.FuncDecl)
	if !ok {
		return "func literal", nil
	}
	if decl.Recv == nil {
		return decl.Name.Name, nil
	}
	var receiver bytes.Buffer
	err := format.Node(&receiver, fset, decl.Recv.List[0].Type)
	return "(" + receiver.String() + ")." + decl.Name.Name, err
}

func complexity(root ast.Node) (int, []ast.Node) {
	cc := 1
	var nested []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok && n != root {
			nested = append(nested, n)
			return false
		}
		switch n := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			cc++
		case *ast.CaseClause:
			if n.List != nil {
				cc++
			}
		case *ast.CommClause:
			if n.Comm != nil {
				cc++
			}
		}
		if isBoolean(n) {
			cc++
		}
		return true
	})
	return cc, nested
}

func lines(fset *token.FileSet, source []byte, node ast.Node, nested []ast.Node) int {
	file := fset.File(node.Pos())
	start, end := file.Offset(node.Pos()), file.Offset(node.End())
	segment := source[start:end]
	set := token.NewFileSet()
	scanFile := set.AddFile("", -1, len(segment))
	var scan scanner.Scanner
	scan.Init(scanFile, segment, nil, 0)
	seen := map[int]bool{}
	for {
		pos, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}
		if kind == token.SEMICOLON && literal == "\n" {
			continue
		}
		absolute := file.Pos(start + scanFile.Offset(pos))
		if slices.ContainsFunc(nested, func(child ast.Node) bool { return absolute >= child.Pos() && absolute < child.End() }) {
			continue
		}
		line := scanFile.PositionFor(pos, false).Line
		for offset := range strings.Count(literal, "\n") + 1 {
			seen[line+offset] = true
		}
	}
	return len(seen)
}

func report(out io.Writer, data measurements, c curve, l limits, top int) error {
	var buffer bytes.Buffer
	w := &buffer
	functions := data.Functions
	fmt.Fprintf(w, "%d functions; %d source LOC; %d files; %d packages\n", len(functions), data.LOC, len(data.Files), len(data.Packages))
	fmt.Fprintln(w, "LOC: gofmt, no comments/blanks; includes declarations and tables, each file line counted once.")
	fmt.Fprintln(w, "All build variants included; generated files excluded; nested literals measured separately.")
	if c.power == 0 {
		fmt.Fprintf(w, "Ceiling = %g (fixed, -power 0)\n", c.at)
	} else {
		fmt.Fprintf(w, "Ceiling = %g + (%g - %g) * (LOC / %g)^%g; integer CC compared to unrounded ceiling\n", c.base, c.at, c.base, c.size, c.power)
	}
	fmt.Fprintf(w, "\nCurve comparison: %g + (%g - %g) * (LOC / %g)^power\n", c.base, c.at, c.base, c.size)
	fmt.Fprintln(w, "power\tviolations\texcess CC\tmax CC/ceiling")
	seen := map[float64]bool{}
	for _, p := range []float64{0, 0.25, 0.5, 0.75, c.power} {
		if seen[p] {
			continue
		}
		seen[p] = true
		candidate := c
		candidate.power = p
		count, excess, worst := 0, 0, 0.0
		for _, f := range functions {
			limit := candidate.ceiling(f.LOC)
			worst = math.Max(worst, float64(f.CC)/limit)
			if float64(f.CC) > limit {
				count++
				excess += f.CC - int(math.Floor(limit))
			}
		}
		fmt.Fprintf(w, "%g\t%d\t%d\t%.2f\n", p, count, excess, worst)
	}
	reportLayers(w, data, l, top)
	sort.SliceStable(functions, func(i, j int) bool {
		return float64(functions[i].CC)/c.ceiling(functions[i].LOC) > float64(functions[j].CC)/c.ceiling(functions[j].LOC)
	})
	fmt.Fprintf(w, "\nTop violations for p=%g:\nLOC\tCC\tceiling\tratio\tfunction\tlocation\n", c.power)
	for _, f := range functions {
		limit := c.ceiling(f.LOC)
		if top == 0 || float64(f.CC) <= limit {
			break
		}
		fmt.Fprintf(w, "%d\t%d\t%.2f\t%.2f\t%s\t%s:%d\n", f.LOC, f.CC, limit, float64(f.CC)/limit, f.Name, f.File, f.Line)
		top--
	}
	tabs := tabwriter.NewWriter(out, 0, tabWidth, tabPadding, ' ', 0)
	if _, err := tabs.Write(buffer.Bytes()); err != nil {
		return err
	}
	return tabs.Flush()
}
