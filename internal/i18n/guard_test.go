package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The guard keeps interface text in the catalogue. It reads the source of
// the packages that print for a person and fails on a string literal that
// reads as English words where text is printed, and on a message key used
// in code that English lacks. It cannot see text that reaches a person by
// another way (a value built elsewhere, an error of another package); the
// boundary of what stays English is described in the README.

// phrase is two words in a row: what interface text looks like.
var phrase = regexp.MustCompile(`[A-Za-z]{2,}[ ,;:]+[A-Za-z]{2,}`)

// keyLike is a string literal that names a message.
var keyLike = regexp.MustCompile(`^(answer|ask|assist|cli|engine|explain|flag|op|page|query|scribe|serve|status|wizard|yes|no)\.[a-z0-9_.\-]+$`)

// printCalls are the calls whose literal arguments a person reads.
var printCalls = map[string]bool{
	"fmt.Fprint": true, "fmt.Fprintf": true, "fmt.Fprintln": true,
	"fmt.Print": true, "fmt.Printf": true, "fmt.Println": true,
	"fmt.Errorf": true, "errors.New": true, "http.Error": true,
}

// guarded are the source files that print for a person, relative to this
// package, and whether their errors are checked too. The errors of the
// scribe assistant are read by a model, and those of an operation come
// from the orders editor, which stays English.
var guarded = map[string]bool{"../cli/*.go": true, "../wizard/*.go": true, "../explain/*.go": true, "../watchtower/*.go": true,
	"../scribe/assist.go": false, "../edit/op.go": false}

// command is the name of a command that leads a message, such as
// "hetman scribe explain:"; it is not text to translate.
var command = regexp.MustCompile(`^hetman(?: [a-z%]+)*:`)

// keep marks a line whose text stays English on purpose.
const keep = "i18n: keep"

func TestNoStrayStrings(t *testing.T) {
	en := Catalogues()[Source]
	fset := token.NewFileSet()
	for pattern, withErrors := range guarded {
		files, err := filepath.Glob(filepath.FromSlash(pattern))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files for %s", pattern)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			kept := map[int]bool{}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if strings.Contains(c.Text, keep) {
						kept[fset.Position(c.Pos()).Line] = true
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if s, ok := literal(x); ok && keyLike.MatchString(s) && !strings.HasSuffix(s, ".") {
						if _, found := en[s]; !found && !composed(s, en) {
							t.Errorf("%s: key %s is not in the English catalogue", fset.Position(x.Pos()), s)
						}
					}
				case *ast.CallExpr:
					name := callName(x.Fun)
					if !printCalls[name] || len(x.Args) == 0 || !withErrors && !strings.HasPrefix(name, "fmt.F") {
						return true
					}
					args := x.Args
					if strings.HasPrefix(name, "fmt.F") {
						if u, ok := args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
							return true // builds text a caller prints, or a model reads
						}
						args = args[1:]
					}
					for _, a := range args {
						lit, ok := a.(*ast.BasicLit)
						if !ok {
							continue
						}
						pos := fset.Position(lit.Pos())
						if s, ok := literal(lit); ok && phrase.MatchString(command.ReplaceAllString(s, "")) && !kept[pos.Line] {
							t.Errorf("%s: text outside the catalogue: %q", pos, s)
						}
					}
				}
				return true
			})
		}
	}
}

// composed reports whether a key is the start of keys built in code, such
// as explain.answered for explain.answered_answers.
func composed(s string, en Catalogue) bool {
	for k := range en {
		if strings.HasPrefix(k, s+"_") {
			return true
		}
	}
	return false
}

func literal(x *ast.BasicLit) (string, bool) {
	if x.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(x.Value)
	return s, err == nil
}

func callName(fun ast.Expr) string {
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name + "." + sel.Sel.Name
		}
	}
	return ""
}

// The page: text is set through t, tn and tk; literal text in the places
// where the page puts text is a stray string.
var pageStray = []*regexp.Regexp{
	regexp.MustCompile(`(?:\btext|\btitle|placeholder|'aria-label'):\s*'[^']*[A-Za-z]{2}`),
	regexp.MustCompile(`\bh\('[a-z0-9]+',\s*(?:null|\{[^{}]*\}),\s*'[^']*[A-Za-z]{2}`),
	regexp.MustCompile(`\btoast\(\s*'`),
	regexp.MustCompile(`'[A-Z][a-z]+ [a-z]+[^']*'`),
	regexp.MustCompile(`'[a-z]+ [a-z]+ [a-z]+ [a-z]+[^']*'`),
}

var pageKey = regexp.MustCompile(`'(page\.[a-z0-9_.]*)'`)

func TestPageTexts(t *testing.T) {
	en := Catalogues()[Source]
	src, err := os.ReadFile(filepath.FromSlash("../watchtower/assets/app.js"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.Contains(line, keep) {
			continue
		}
		for _, re := range pageStray {
			if m := re.FindString(line); m != "" {
				t.Errorf("app.js:%d: text outside the catalogue: %s", i+1, m)
			}
		}
		for _, m := range pageKey.FindAllStringSubmatch(line, -1) {
			if k := m[1]; !strings.HasSuffix(k, ".") {
				if _, ok := en[k]; !ok {
					t.Errorf("app.js:%d: key %s is not in the English catalogue", i+1, k)
				}
			}
		}
	}
	html, err := os.ReadFile(filepath.FromSlash("../watchtower/assets/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`>([^<]*)<`).FindAllStringSubmatch(string(html), -1) {
		if s := strings.TrimSpace(m[1]); s != "" && !regexp.MustCompile(`^\{\{\.[A-Za-z]+\}\}$`).MatchString(s) {
			t.Errorf("index.html: text outside the catalogue: %q", s)
		}
	}
}
