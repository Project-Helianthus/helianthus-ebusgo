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
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type goPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	// IgnoredGoFiles are excluded by build constraints, so go list omits their imports from Imports.
	IgnoredGoFiles []string
	Imports        []string
}

// vendorNeutralRoots are the directories whose packages must not import a driver, SemReg, host or vendor package.
var vendorNeutralRoots = []string{"adaptermux", "engine", "localworker", "protocol/responder", "transport"}

// forbiddenImportPrefixes are import paths that a vendor-neutral transport package must not use.
var forbiddenImportPrefixes = []string{
	"github.com/Project-Helianthus/helianthus-ebusreg",
	"github.com/Project-Helianthus/helianthus-semreg",
	"github.com/Project-Helianthus/helianthus-ebusgateway",
	"github.com/Project-Helianthus/helianthus-gateway",
	"github.com/Project-Helianthus/helianthus-matter-binding",
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol/vaillant",
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

	if err := checkVendorNeutralImports(packages); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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

func checkVendorNeutralImports(packages []goPackage) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("public symbol gate: %v", err)
	}
	for _, pkg := range packages {
		rel, err := filepath.Rel(cwd, pkg.Dir)
		if err != nil {
			return fmt.Errorf("public symbol gate: %v", err)
		}
		rel = filepath.ToSlash(rel)
		if !inVendorNeutralRoot(rel) {
			continue
		}
		for _, imp := range pkg.Imports {
			if isForbiddenImport(imp) {
				return fmt.Errorf("public symbol gate: vendor-neutral path imports %s (%s)", imp, pkg.ImportPath)
			}
		}
		for _, name := range pkg.IgnoredGoFiles {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			imports, err := fileImports(filepath.Join(pkg.Dir, name))
			if err != nil {
				return fmt.Errorf("public symbol gate: %v", err)
			}
			for _, imp := range imports {
				if isForbiddenImport(imp) {
					return fmt.Errorf("public symbol gate: vendor-neutral path imports %s (%s)", imp, pkg.ImportPath)
				}
			}
		}
	}
	return checkVendorNeutralFiles(cwd)
}

// checkVendorNeutralFiles checks every production file under the vendor-neutral roots, independently of the
// package list: go list omits a package whose files are all excluded by build constraints.
func checkVendorNeutralFiles(cwd string) error {
	modulePath, err := currentModulePath()
	if err != nil {
		return err
	}
	for _, root := range vendorNeutralRoots {
		err := filepath.WalkDir(filepath.Join(cwd, filepath.FromSlash(root)), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			imports, err := fileImports(path)
			if err != nil {
				return err
			}
			dir, err := filepath.Rel(cwd, filepath.Dir(path))
			if err != nil {
				return err
			}
			pkgPath := modulePath + "/" + filepath.ToSlash(dir)
			for _, imp := range imports {
				if isForbiddenImport(imp) {
					return fmt.Errorf("public symbol gate: vendor-neutral path imports %s (%s)", imp, pkgPath)
				}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("public symbol gate: %v", err)
		}
	}
	return nil
}

func currentModulePath() (string, error) {
	out, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		return "", fmt.Errorf("public symbol gate: go list -m failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// fileImports returns the import paths of one Go file, including files excluded by build constraints.
func fileImports(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var imports []string
	for _, spec := range file.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		imports = append(imports, imp)
	}
	return imports, nil
}

func inVendorNeutralRoot(rel string) bool {
	for _, root := range vendorNeutralRoots {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

func isForbiddenImport(imp string) bool {
	for _, prefix := range forbiddenImportPrefixes {
		if strings.HasPrefix(imp, prefix) {
			return true
		}
	}
	// Any other helianthus-*reg registry module.
	const modulePrefix = "github.com/Project-Helianthus/helianthus-"
	if !strings.HasPrefix(imp, modulePrefix) {
		return false
	}
	module := strings.TrimPrefix(imp, modulePrefix)
	if idx := strings.IndexByte(module, '/'); idx >= 0 {
		module = module[:idx]
	}
	return strings.HasSuffix(module, "reg")
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
