package research_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResearchImportBoundary(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	targets := []string{filepath.Join(root, "internal", "research"), filepath.Join(root, "internal", "adapters", "researchdata"), filepath.Join(root, "cmd", "tradeedge-research")}
	banned := []string{"/broker", "/execution", "/shadowruntime", "/tradingruntime", "/adapters/broker", "/integration/zerodha", "upstox", "net/http"}
	for _, target := range targets {
		err := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, imported := range file.Imports {
				name := strings.Trim(imported.Path.Value, "\"")
				for _, denied := range banned {
					if strings.Contains(name, denied) {
						t.Errorf("%s imports prohibited package %s", path, name)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
