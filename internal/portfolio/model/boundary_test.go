package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPhase3ForbiddenImportsAndAuthoritativeTypes(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, relative := range []string{
		"internal/portfolio", "internal/risk",
		"internal/adapters/portfolio", "internal/adapters/risk",
	} {
		path := filepath.Join(root, relative)
		err := filepath.WalkDir(path, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(filename) != ".go" || strings.HasSuffix(filename, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			for _, imported := range file.Imports {
				value, _ := strconv.Unquote(imported.Path.Value)
				for _, forbidden := range []string{
					"/execution", "/broker", "zerodha", "/reconciliation", "net/http",
					"prometheus",
				} {
					if forbidden == "net/http" && strings.Contains(filepath.ToSlash(filename), "/risk/opshttp/") {
						continue
					}
					if strings.Contains(strings.ToLower(value), forbidden) {
						t.Errorf("%s imports forbidden capability %q", filename, value)
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.StructType:
					for _, field := range value.Fields.List {
						if identifier, ok := field.Type.(*ast.Ident); ok &&
							(identifier.Name == "float32" || identifier.Name == "float64") {
							t.Errorf("%s contains authoritative %s field", filename, identifier.Name)
						}
						for _, name := range field.Names {
							lower := strings.ToLower(name.Name)
							for _, forbidden := range []string{
								"brokertoken", "orderpayload", "orderrequest", "accountid",
								"credential", "apisecret", "fillstate",
							} {
								if strings.Contains(lower, forbidden) {
									t.Errorf("%s exposes forbidden field %s", filename, name.Name)
								}
							}
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
