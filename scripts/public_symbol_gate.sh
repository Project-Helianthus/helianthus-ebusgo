#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/public_symbol_gate.go" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type goPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
}

type publicSymbol struct {
	ImportPath string
	File       string
	Name       string
	Kind       string
}

func main() {
	packages, err := listPackages()
	if err != nil {
		fatalf("public symbol gate: %v", err)
	}

	symbols, err := collectPublicSymbols(packages)
	if err != nil {
		fatalf("public symbol gate: %v", err)
	}

	failures := legacySelectionSymbols(symbols)
	if len(failures) > 0 {
		fmt.Fprintln(os.Stderr, "public symbol gate: legacy source-address selection symbols remain:")
		for _, symbol := range failures {
			fmt.Fprintf(os.Stderr, "  %s.%s (%s, %s)\n", symbol.ImportPath, symbol.Name, symbol.Kind, filepath.ToSlash(symbol.File))
		}
		os.Exit(1)
	}

	required := map[string]bool{
		"github.com/Project-Helianthus/helianthus-ebusgo/protocol.SourceAddressSelector":    false,
		"github.com/Project-Helianthus/helianthus-ebusgo/protocol.NewSourceAddressSelector": false,
	}
	for _, symbol := range symbols {
		key := symbol.ImportPath + "." + symbol.Name
		if _, ok := required[key]; ok {
			required[key] = true
		}
	}
	for key, seen := range required {
		if !seen {
			fmt.Fprintf(os.Stderr, "public symbol gate: required active selection contract missing: %s\n", key)
			os.Exit(1)
		}
	}
}

func listPackages() ([]goPackage, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("go list failed: %w", err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	var packages []goPackage
	for {
		var pkg goPackage
		if err := dec.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

func collectPublicSymbols(packages []goPackage) ([]publicSymbol, error) {
	var symbols []publicSymbol
	for _, pkg := range packages {
		for _, name := range pkg.GoFiles {
			filePath := filepath.Join(pkg.Dir, name)
			fileSet := token.NewFileSet()
			file, err := parser.ParseFile(fileSet, filePath, nil, 0)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", filePath, err)
			}
			cwd, err := os.Getwd()
			if err != nil {
				cwd = "."
			}
			relFile, err := filepath.Rel(cwd, filePath)
			if err != nil {
				relFile = filePath
			}
			symbols = append(symbols, exportedTopLevelSymbols(pkg.ImportPath, relFile, file)...)
		}
	}
	return symbols, nil
}

func exportedTopLevelSymbols(importPath, filePath string, file *ast.File) []publicSymbol {
	var symbols []publicSymbol
	add := func(kind, name string) {
		if ast.IsExported(name) {
			symbols = append(symbols, publicSymbol{
				ImportPath: importPath,
				File:       filePath,
				Name:       name,
				Kind:       kind,
			})
		}
	}

	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Recv == nil {
				add("func", decl.Name.Name)
			} else {
				add("method", decl.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					add("type", spec.Name.Name)
					symbols = append(symbols, exportedMembers(importPath, filePath, spec)...)
				case *ast.ValueSpec:
					kind := "var"
					if decl.Tok == token.CONST {
						kind = "const"
					}
					for _, name := range spec.Names {
						add(kind, name.Name)
					}
				}
			}
		}
	}
	return symbols
}

func exportedMembers(importPath, filePath string, spec *ast.TypeSpec) []publicSymbol {
	var symbols []publicSymbol
	add := func(kind, name string) {
		if ast.IsExported(name) {
			symbols = append(symbols, publicSymbol{
				ImportPath: importPath,
				File:       filePath,
				Name:       spec.Name.Name + "." + name,
				Kind:       kind,
			})
		}
	}

	switch typ := spec.Type.(type) {
	case *ast.StructType:
		for _, field := range typ.Fields.List {
			for _, name := range field.Names {
				add("field", name.Name)
			}
		}
	case *ast.InterfaceType:
		for _, method := range typ.Methods.List {
			for _, name := range method.Names {
				add("interface method", name.Name)
			}
		}
	}
	return symbols
}

func legacySelectionSymbols(symbols []publicSymbol) []publicSymbol {
	fragments := []string{"join", "gentle", "admission"}
	var failures []publicSymbol
	for _, symbol := range symbols {
		leaf := symbol.Name
		if idx := strings.LastIndexByte(leaf, '.'); idx >= 0 {
			leaf = leaf[idx+1:]
		}
		lower := strings.ToLower(leaf)
		for _, fragment := range fragments {
			if strings.Contains(lower, fragment) {
				failures = append(failures, symbol)
				break
			}
		}
	}
	return failures
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
GO

go run "$tmpdir/public_symbol_gate.go"
