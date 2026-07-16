package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allowedDiagnosticKeys = map[string]struct{}{
	"duration_ms":       {},
	"error_code":        {},
	"method":            {},
	"outcome":           {},
	"request_id":        {},
	"reason_code":       {},
	"response_bytes":    {},
	"route":             {},
	"stack_fingerprint": {},
	"status":            {},
}

var loggingMethods = map[string]struct{}{
	"Debug": {}, "DPanic": {}, "Error": {}, "Fatal": {}, "Info": {},
	"Log": {}, "Named": {}, "Panic": {}, "Sugar": {}, "Warn": {}, "With": {},
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	return root
}

func productionFiles(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	require.NoError(t, err)
	return files
}

func parseFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	return file
}

func TestOnlyDiagnosticAdapterImportsZap(t *testing.T) {
	root := moduleRoot(t)
	for _, path := range productionFiles(t) {
		relative, err := filepath.Rel(root, path)
		require.NoError(t, err)
		adapter := relative == filepath.Join("diagnostic", "logger.go")
		for _, imported := range parseFile(t, path).Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			require.NoError(t, err)
			if importPath == "go.uber.org/zap" && !adapter {
				assert.Failf(t, "direct Zap import", "%s imports %s", relative, importPath)
			}
			if strings.HasPrefix(importPath, "go.uber.org/zap/") {
				assert.Failf(t, "Zap subpackage import", "%s imports forbidden %s", relative, importPath)
			}
			if importPath == "github.com/belLena81/raglibrarian/pkg/logger" && relative != filepath.Join("cmd", "main.go") {
				assert.Failf(t, "project logger import", "%s imports the project logger outside composition", relative)
			}
			if importPath == "log" || importPath == "log/slog" {
				assert.Failf(t, "unrestricted logging import", "%s imports %s", relative, importPath)
			}
		}
	}
}

func TestOnlyDiagnosticAdapterEmitsApplicationLogs(t *testing.T) {
	root := moduleRoot(t)
	for _, path := range productionFiles(t) {
		relative, err := filepath.Rel(root, path)
		require.NoError(t, err)
		if relative == filepath.Join("diagnostic", "logger.go") {
			continue
		}
		file := parseFile(t, path)
		imports := importAliases(t, file)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, isLoggingMethod := loggingMethods[selector.Sel.Name]; !isLoggingMethod {
				return true
			}
			if selector.Sel.Name == "Error" {
				if receiver, ok := selector.X.(*ast.Ident); ok && imports[receiver.Name] == "net/http" {
					return true
				}
			}
			assert.Failf(t, "direct logger call", "%s calls %s outside diagnostic adapter", relative, selector.Sel.Name)
			return true
		})
	}
}

func importAliases(t *testing.T, file *ast.File) map[string]string {
	t.Helper()
	aliases := make(map[string]string, len(file.Imports))
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		require.NoError(t, err)
		name := filepath.Base(importPath)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		aliases[name] = importPath
	}
	return aliases
}

func TestDiagnosticAdapterUsesOnlyAllowlistedZapFields(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "diagnostic", "logger.go")
	file := parseFile(t, path)
	allowedConstructors := map[string]struct{}{"Bool": {}, "Int": {}, "Int64": {}, "String": {}}
	allowedEvents := map[string]struct{}{
		"auth.login.failed":           {},
		"auth.register.failed":        {},
		"auth.token.rejected":         {},
		"http.panic.recovered":        {},
		"http.request_id.failed":      {},
		"http.request.completed":      {},
		"query.retrieval.unavailable": {},
		"service.run.failed":          {},
		"service.start.failed":        {},
	}
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		require.NoError(t, err)
		if importPath == "go.uber.org/zap" {
			assert.Nil(t, imported.Name, "Zap import aliases are forbidden")
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		selectorNode, ok := node.(*ast.SelectorExpr)
		if ok {
			packageName, packageOK := selectorNode.X.(*ast.Ident)
			if packageOK && packageName.Name == "zap" && selectorNode.Sel.Name == "Field" {
				assert.Fail(t, "direct zap.Field types are forbidden; use allowlisted constructors at the log call")
			}
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if ok && packageName.Name == "zap" {
			validateZapConstructor(t, call, selector.Sel.Name, allowedConstructors)
			return true
		}
		if _, isLoggingMethod := loggingMethods[selector.Sel.Name]; !isLoggingMethod {
			return true
		}
		if !assert.NotEmpty(t, call.Args, "diagnostic log call requires a fixed event") {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !assert.True(t, ok, "diagnostic event must be a string literal") {
			return true
		}
		event, err := strconv.Unquote(literal.Value)
		require.NoError(t, err)
		_, allowed := allowedEvents[event]
		assert.True(t, allowed, "diagnostic event %q is not allowlisted", event)
		for _, argument := range call.Args[1:] {
			fieldCall, ok := argument.(*ast.CallExpr)
			if !assert.True(t, ok, "diagnostic fields must be direct allowlisted Zap constructors") {
				continue
			}
			fieldSelector, ok := fieldCall.Fun.(*ast.SelectorExpr)
			if !assert.True(t, ok, "diagnostic fields must be direct Zap constructors") {
				continue
			}
			fieldPackage, ok := fieldSelector.X.(*ast.Ident)
			if !assert.True(t, ok && fieldPackage.Name == "zap", "diagnostic fields must use the default Zap import") {
				continue
			}
			validateZapConstructor(t, fieldCall, fieldSelector.Sel.Name, allowedConstructors)
		}
		return true
	})
}

func validateZapConstructor(t *testing.T, call *ast.CallExpr, name string, allowed map[string]struct{}) {
	t.Helper()
	if _, ok := allowed[name]; !ok {
		assert.Failf(t, "unsafe Zap constructor", "zap.%s is not allowed", name)
		return
	}
	if !assert.NotEmpty(t, call.Args, "zap.%s must have a literal field key", name) {
		return
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !assert.True(t, ok, "zap.%s field key must be a string literal", name) {
		return
	}
	key, err := strconv.Unquote(literal.Value)
	require.NoError(t, err)
	_, keyAllowed := allowedDiagnosticKeys[key]
	assert.True(t, keyAllowed, "diagnostic field key %q is not allowlisted", key)
}
