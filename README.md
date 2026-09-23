# budget

Budget measures Go source code for an experiment: minimise lines of code while keeping each function within fixed complexity limits. It reports source LOC alongside cyclomatic complexity, nesting depth, and boolean-expression size, with a check mode for use in a coding agent's feedback loop.

The default limits are CC <= 10, nesting <= 3, and at most three `&&` or `||` operators in one expression. Budget reports LOC without imposing a length limit or reducing code automatically; choosing the shortest acceptable implementation is the task given to the agent.

Extracting branches into a helper can clear a function's CC warning without removing any decisions. Budget also sums `CC - 1` by file and package: a function with CC 13 and two functions with CC 7 both contribute 12 decisions. This total stays unchanged when the same branches are extracted within its scope, so it distinguishes redistribution from branch removal. Moving a helper to another file changes the file totals but preserves the package total if it stays in that package.

## Usage

Run from this checkout with the Go toolchain required by [go.mod](go.mod). Budget uses only the standard library and parses the target source without compiling it.

```sh
go run . .
go run . ../git-pkgs
go run . -check ../git-pkgs
go run . -json ../git-pkgs > /tmp/budget.json
go test ./...
```

The directory defaults to `.`. Reporting exits successfully even when limits are exceeded; `-check` returns exit code 1 for violations. Input and output errors also return exit code 1. `-h` and `-help` print usage to stdout and exit successfully without writing to stderr.

```sh
go run . -at 15 -nesting 4 -boolean 3 ../git-pkgs
go run . -file-decisions 40 ../git-pkgs
go run . -tests -top 10 ../git-pkgs
```

`-at` sets the fixed CC ceiling. File decision budgets are optional: `-file-decisions 40` flags files whose sum of `CC - 1` exceeds 40, while the default of 0 leaves file totals informational. Package totals are always informational. `-top` limits displayed rows without changing the checks or JSON output.

The text report includes violations, file and package totals, and comparisons with several LOC-dependent ceilings. JSON contains `LOC`, `Functions`, `Files`, `Packages`, and `Violations`; the total violation count counts each failing function once, plus any failing files. File paths are relative to the scanned directory in both formats. JSON package keys use `directory:package` to distinguish packages sharing a directory. Text labels use the directory when its basename matches the package name, otherwise showing both, such as `main (cmd/brief)` or `brief (root)`.

## Measurements

Source LOC counts token-bearing lines after Go formatting, including imports, declarations, lookup tables, and multiline literals. Comments and blank lines outside literals are excluded. Each file line contributes once to the source total, so moving code outside a function does not remove it from the objective. LOC uses physical lines in the formatted source; reported function locations use physical lines in the original file. `//line` directives do not change either measurement.

CC starts at 1 and adds one for each `if`, loop, non-default switch/select case, and `&&` or `||`. A case with several comma-separated values counts once. Anonymous functions receive separate measurements, with their branches excluded from the enclosing function's CC.

Nesting counts enclosing `if`, `for`, `for range`, `switch`, type switch, and `select` structures. An `else if` chain occupies one level, and anonymous functions start their own nesting count. Boolean-expression size counts `&&` and `||` across the whole expression, including both sides of a comparison: `(a && b && c) == (d && e && f)` counts as four operators. Wrapping an expression across lines does not change its score. Separate expressions and nested anonymous functions are measured independently.

File and package decision totals sum `CC - 1` across functions. Subtracting the initial 1 avoids adding a decision merely because a helper exists; extracting the same branches into helpers leaves this total unchanged. Packages are grouped by directory and package name.

By default, scans exclude tests, generated files, hidden subdirectories, and directories named `vendor`, `testdata`, or `scripts`. `-tests` includes test files; `-exclude` replaces the comma-separated directory exclusion list, trims surrounding whitespace, and ignores empty entries. All build variants are scanned, regardless of build tags or target platform.

For experiments with a rising ceiling, use `-power`:

```text
CC_max(LOC) = base + (at - base) * (LOC / reference_LOC)^power
```

```sh
go run . -base 1 -at 10 -loc 40 -power 0.5 ../git-pkgs
```

The default power of 0 gives a fixed ceiling of 10. Powers between 0 and 1 give sublinear curves; CC is compared against the unrounded ceiling. These curves allow larger functions more branching, but added code can also buy compliance.

## Using it with an agent

A starting instruction is:

> Implement the required behavior with the fewest formatted production source lines. Keep CC <= 10, nesting <= 3, and boolean operators per expression <= 3. Count all added helpers and declarations. Preserve behavior and tests, and run budget with `-check` after each revision.

Keep the task, scan scope, and acceptance tests fixed when comparing implementations. Budget checks the entire supplied directory; it has no baseline or changed-code mode. Existing violations therefore need separate handling before using it to gate new work.

Use budget alongside correctness linters and tests. Tools such as `gocyclo`, `gocognit`, and `funlen` already cover much of the same ground, with different counting rules. Budget combines the constraints with whole-source LOC and decision totals for comparing implementations; it does not check correctness, duplication, dependency vulnerabilities, or whether a shorter implementation is easier to maintain.

## License

[MIT](LICENSE).
