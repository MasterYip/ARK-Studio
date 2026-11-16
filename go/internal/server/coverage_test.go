package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotPortedHasOneCaller: the 501 NOT_PORTED reply is reachable only
// through unknownOrNotPorted (a contract route that no handler took).  The
// route probe (tests/python/contract/replay_probe.py) sends one request per
// Python dispatch branch, so that fallback is exercised for every branch.
func TestNotPortedHasOneCaller(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var callers []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "notPorted" {
							callers = append(callers, fn.Name.Name)
						}
					}
					if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, `"NOT_PORTED"`) && fn.Name.Name != "notPorted" {
						callers = append(callers, fn.Name.Name+" (literal)")
					}
					return true
				})
			}
		}
	}
	if len(callers) != 1 || callers[0] != "unknownOrNotPorted" {
		t.Fatalf("NOT_PORTED must only be answered by unknownOrNotPorted; found in %v", callers)
	}
}

// TestRecordedProbeHasNoNotPorted reads the recorded route probe
// (docs/development/go-port/probe.json): no probe request may have answered
// NOT_PORTED, and every dispatcher record of the contract must be probed.
func TestRecordedProbeHasNoNotPorted(t *testing.T) {
	docs := filepath.Join("..", "..", "..", "docs", "development", "go-port")
	raw, err := os.ReadFile(filepath.Join(docs, "probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Probes map[string][]any `json:"probes"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{}
	for key, v := range probe.Probes {
		if len(v) == 0 {
			t.Fatalf("empty probe row %q", key)
		}
		for _, id := range strings.Split(v[0].(string), " ; ") {
			groups[id] = true
		}
		for _, status := range v[1:] {
			if status == "NOT_PORTED" {
				t.Errorf("probe %q answered NOT_PORTED", key)
			}
		}
	}
	craw, err := os.ReadFile(filepath.Join(docs, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Routes []struct {
			ID, Role string
		} `json:"routes"`
	}
	if err := json.Unmarshal(craw, &contract); err != nil {
		t.Fatal(err)
	}
	for _, r := range contract.Routes {
		if !groups[r.ID] {
			t.Errorf("contract record %s (%s) is not in the recorded probe", r.ID, r.Role)
		}
	}
}
