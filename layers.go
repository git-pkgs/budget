package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

type limits struct {
	BooleanOps    int
	Nesting       int
	FileDecisions int
}

type aggregate struct {
	Name          string
	Package       string
	LOC           int
	FunctionCount int
	Decisions     int
}

type fileMeasurement struct {
	aggregate
	Functions []function
}

type violations struct {
	CC         int
	Nesting    int
	BooleanOps int
	Files      int
	Total      int
}

type measurements struct {
	LOC        int
	Functions  []function
	Files      []aggregate
	Packages   []aggregate
	Violations violations
}

func (m *measurements) aggregatePackages() {
	packages := map[string]aggregate{}
	for _, file := range m.Files {
		p := packages[file.Package]
		p.Name = file.Package
		p.LOC += file.LOC
		p.FunctionCount += file.FunctionCount
		p.Decisions += file.Decisions
		packages[file.Package] = p
		m.LOC += file.LOC
	}
	for _, p := range packages {
		m.Packages = append(m.Packages, p)
	}
	sort.Slice(m.Packages, func(i, j int) bool { return m.Packages[i].Name < m.Packages[j].Name })
}

func (m *measurements) evaluate(c curve, l limits) {
	for _, f := range m.Functions {
		m.Violations.addFunction(f, c, l)
	}
	for _, f := range m.Files {
		if l.FileDecisions > 0 && f.Decisions > l.FileDecisions {
			m.Violations.Files++
			m.Violations.Total++
		}
	}
}

func (v *violations) addFunction(f function, c curve, l limits) {
	before := v.CC + v.Nesting + v.BooleanOps
	if float64(f.CC) > c.ceiling(f.LOC) {
		v.CC++
	}
	if f.Nesting > l.Nesting {
		v.Nesting++
	}
	if f.BooleanOps > l.BooleanOps {
		v.BooleanOps++
	}
	if v.CC+v.Nesting+v.BooleanOps > before {
		v.Total++
	}
}

func localComplexity(root ast.Node) (int, int) {
	maximum, boolean := 0, 0
	ast.PreorderStack(root, nil, func(n ast.Node, parents []ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok && n != root {
			return false
		}
		depth := 0
		for i, ancestor := range append(parents, n) {
			if isControl(ancestor) && (i == 0 || !isElseIf(parents[i-1], ancestor)) {
				depth++
			}
		}
		maximum = max(maximum, depth)
		if _, ok := n.(ast.Expr); ok {
			boolean = max(boolean, booleanOperators(n))
		}
		return true
	})
	return maximum, boolean
}

func isControl(n ast.Node) bool {
	switch n.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		return true
	}
	return false
}

func isElseIf(parent, child ast.Node) bool {
	branch, ok := parent.(*ast.IfStmt)
	return ok && branch.Else == child
}

func booleanOperators(root ast.Node) int {
	count := 0
	ast.Inspect(root, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if isBoolean(n) {
			count++
		}
		return true
	})
	return count
}

func isBoolean(n ast.Node) bool {
	expr, ok := n.(*ast.BinaryExpr)
	return ok && (expr.Op == token.LAND || expr.Op == token.LOR)
}

func reportLayers(out *bytes.Buffer, data measurements, l limits, top int) {
	v := data.Violations
	fmt.Fprintf(out, "\nConfigured limits: boolean operators <= %d, nesting <= %d", l.BooleanOps, l.Nesting)
	if l.FileDecisions > 0 {
		fmt.Fprintf(out, ", file decisions <= %d", l.FileDecisions)
	}
	fmt.Fprintf(out, "\nViolations: CC=%d, nesting=%d, boolean=%d, files=%d; %d distinct functions/files\n", v.CC, v.Nesting, v.BooleanOps, v.Files, v.Total)
	reportLocal(out, data.Functions, l, top)
	reportAggregates(out, "Files", data.Files, top)
	reportAggregates(out, "Packages", data.Packages, top)
}

func reportLocal(out *bytes.Buffer, functions []function, l limits, top int) {
	functions = append([]function(nil), functions...)
	sort.SliceStable(functions, func(i, j int) bool {
		return max(functions[i].Nesting-l.Nesting, functions[i].BooleanOps-l.BooleanOps) > max(functions[j].Nesting-l.Nesting, functions[j].BooleanOps-l.BooleanOps)
	})
	fmt.Fprintln(out, "\nLocal violations:\nnesting\tboolean ops\tfunction\tlocation")
	for _, f := range functions {
		if top == 0 || (f.Nesting <= l.Nesting && f.BooleanOps <= l.BooleanOps) {
			break
		}
		fmt.Fprintf(out, "%d\t%d\t%s\t%s:%d\n", f.Nesting, f.BooleanOps, f.Name, f.File, f.Line)
		top--
	}
}

func reportAggregates(out *bytes.Buffer, label string, groups []aggregate, top int) {
	groups = append([]aggregate(nil), groups...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Decisions > groups[j].Decisions })
	fmt.Fprintf(out, "\n%s by decision total (sum of CC-1):\ndecisions\tLOC\tfunctions\tname\n", label)
	for _, group := range groups {
		if top == 0 {
			break
		}
		fmt.Fprintf(out, "%d\t%d\t%d\t%s\n", group.Decisions, group.LOC, group.FunctionCount, group.displayName())
		top--
	}
}

func (a aggregate) displayName() string {
	if a.Package != "" {
		return a.Name
	}
	dir, name, _ := strings.Cut(a.Name, ":")
	if filepath.Base(dir) == name {
		return dir
	}
	if dir == "." {
		dir = "root"
	}
	return name + " (" + dir + ")"
}
