package application

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	internalImportMarker = "/internal/"
	moduleImportMarker   = "/internal/modules/"
)

var (
	createTablePattern   = regexp.MustCompile("(?i)\\bCREATE\\s+TABLE(?:\\s+IF\\s+NOT\\s+EXISTS)?\\s+[`\"]?([a-z_][a-z0-9_]*)[`\"]?")
	sqlIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

func TestKernelDoesNotOwnHostDependencies(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse application package: %v", err)
	}
	banned := []string{"os/signal", "github.com/wailsapp/"}
	for _, parsedPackage := range packages {
		for filename, file := range parsedPackage.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.ImportSpec:
					importPath, err := strconv.Unquote(typed.Path.Value)
					if err != nil {
						t.Fatalf("unquote import in %s: %v", filename, err)
					}
					for _, prefix := range banned {
						if strings.HasPrefix(importPath, prefix) {
							t.Errorf("application kernel imports host dependency %q in %s", importPath, filename)
						}
					}
				case *ast.CallExpr:
					selector, ok := typed.Fun.(*ast.SelectorExpr)
					if ok && strings.HasPrefix(selector.Sel.Name, "Listen") {
						t.Errorf("application kernel owns listener call %s in %s", selector.Sel.Name, filename)
					}
				}
				return true
			})
		}
	}
}

func TestProductionModulesDoNotImportOtherModules(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	violations, err := productionModuleImportViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect production module imports: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("production modules must collaborate through consumer ports injected by internal/app; cross-module imports:\n%s", strings.Join(violations, "\n"))
	}
}

func TestLowLevelBackendPackagesDoNotDependOnHigherLayers(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	violations, err := productionBackendLayerViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect backend layers: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("backend low-level packages must not depend on higher layers and app may contain only composition packages:\n%s", strings.Join(violations, "\n"))
	}
}

func TestBackendLayerFixtureRejectsReverseDependencies(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("internal/contracts/shared/value.go", "package shared\nimport _ \"example.test/product/internal/modules/orders\"\n")
	writeFixture("internal/application/service.go", "package application\nimport _ \"example.test/product/internal/platform/database\"\n")
	writeFixture("internal/platform/client.go", "package platform\nimport _ \"example.test/product/internal/app/product\"\n")
	writeFixture("internal/platform/observability/handler.go", "package observability\n")
	writeFixture("internal/app/runtime/runtime.go", "package runtime\n")
	writeFixture("internal/app/product/product.go", "package product\nimport _ \"example.test/product/internal/modules/orders\"\n")

	violations, err := productionBackendLayerViolations(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"internal/app/runtime/runtime.go: app package runtime is not a composition package",
		"internal/application/service.go: application imports platform",
		"internal/contracts/shared/value.go: contracts imports modules",
		"internal/platform/client.go: platform imports app",
		"internal/platform/observability/handler.go: obsolete backend package platform/observability",
	}
	if fmt.Sprint(violations) != fmt.Sprint(want) {
		t.Fatalf("violations = %v, want %v", violations, want)
	}
}

func TestDesktopSidecarCommandContainsOnlyProcessEntry(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	violations, err := desktopSidecarCommandViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect Desktop sidecar command: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("cmd/desktop-sidecar may contain only its process entry; runtime ownership belongs under internal/host:\n%s", strings.Join(violations, "\n"))
	}
}

func TestDesktopSidecarCommandFixtureRejectsRuntimeOwnership(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("cmd/desktop-sidecar/main.go", "package main\nimport _ \"example.test/product/internal/platform/database\"\nfunc main() {}\n")
	writeFixture("cmd/desktop-sidecar/main_test.go", "package main\n")
	writeFixture("cmd/desktop-sidecar/runtime.go", "package main\n")

	violations, err := desktopSidecarCommandViolations(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cmd/desktop-sidecar/main.go: Desktop entry imports runtime dependency example.test/product/internal/platform/database",
		"cmd/desktop-sidecar/runtime.go: Desktop command owns non-entry production code",
	}
	if fmt.Sprint(violations) != fmt.Sprint(want) {
		t.Fatalf("violations = %v, want %v", violations, want)
	}
}

func TestServerCommandContainsOnlyProcessEntry(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	violations, err := serverCommandViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect Server command: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("cmd/server may contain only its process entry; runtime composition belongs under internal/app/product:\n%s", strings.Join(violations, "\n"))
	}
}

func TestServerCommandFixtureRejectsRuntimeOwnership(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("cmd/server/main.go", "package main\nimport (\n_ \"database/sql\"\n_ \"net/http\"\n_ \"example.test/product/internal/platform/database\"\n)\nfunc main() {}\n")
	writeFixture("cmd/server/main_test.go", "package main\n")
	writeFixture("cmd/server/runtime.go", "package main\n")

	violations, err := serverCommandViolations(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cmd/server/main.go: Server entry imports runtime dependency database/sql",
		"cmd/server/main.go: Server entry imports runtime dependency example.test/product/internal/platform/database",
		"cmd/server/main.go: Server entry imports runtime dependency net/http",
		"cmd/server/runtime.go: Server command owns non-entry production code",
	}
	if fmt.Sprint(violations) != fmt.Sprint(want) {
		t.Fatalf("violations = %v, want %v", violations, want)
	}
}

func TestLegacyServerCommandsAreRemoved(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	for _, directory := range []string{"cmd/config-check", "cmd/migrate"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(directory))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy command %s still exists", directory)
		}
	}
}

func desktopSidecarCommandViolations(root fs.FS) ([]string, error) {
	return commandViolations(root, "cmd/desktop-sidecar", "Desktop")
}

func serverCommandViolations(root fs.FS) ([]string, error) {
	return commandViolations(root, "cmd/server", "Server")
}

func commandViolations(root fs.FS, commandDirectory, commandName string) ([]string, error) {
	var violations []string
	mainFilename := commandDirectory + "/main.go"
	content, err := fs.ReadFile(root, mainFilename)
	if err != nil {
		return nil, err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), mainFilename, content, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	for _, specification := range parsed.Imports {
		importPath, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return nil, err
		}
		if commandEntryRuntimeImport(importPath) {
			violations = append(violations, fmt.Sprintf("%s: %s entry imports runtime dependency %s", mainFilename, commandName, importPath))
		}
	}
	err = fs.WalkDir(root, commandDirectory, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filename) != ".go" || strings.HasSuffix(filename, "_test.go") || filename == commandDirectory+"/main.go" {
			return nil
		}
		violations = append(violations, fmt.Sprintf("%s: %s command owns non-entry production code", filename, commandName))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func commandEntryRuntimeImport(importPath string) bool {
	if importPath == "database/sql" || importPath == "net" || importPath == "net/http" {
		return true
	}
	for _, marker := range []string{
		"/internal/application",
		"/internal/host/lifecycle",
		"/internal/modules/",
		"/internal/platform/database",
	} {
		if strings.Contains(importPath, marker) {
			return true
		}
	}
	return false
}

func productionBackendLayerViolations(root fs.FS) ([]string, error) {
	allowedTargets := map[string]map[string]struct{}{
		"application": {"application": {}, "contracts": {}},
		"contracts":   {"contracts": {}},
		"host":        {"application": {}, "contracts": {}, "host": {}, "platform": {}},
		"platform":    {"contracts": {}, "platform": {}},
	}
	compositionPackages := map[string]struct{}{"adapters": {}, "product": {}}
	obsoletePackages := map[string]struct{}{"platform/observability": {}}
	var violations []string
	err := fs.WalkDir(root, "internal", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filename) != ".go" || strings.HasSuffix(filename, "_test.go") {
			return nil
		}
		parts := strings.Split(filename, "/")
		if len(parts) < 3 {
			return nil
		}
		sourceLayer := parts[1]
		if len(parts) >= 4 {
			packagePath := parts[1] + "/" + parts[2]
			if _, obsolete := obsoletePackages[packagePath]; obsolete {
				violations = append(violations, fmt.Sprintf("%s: obsolete backend package %s", filename, packagePath))
			}
		}
		if sourceLayer == "app" && len(parts) >= 4 {
			if _, allowed := compositionPackages[parts[2]]; !allowed {
				violations = append(violations, fmt.Sprintf("%s: app package %s is not a composition package", filename, parts[2]))
			}
		}
		allowed, constrained := allowedTargets[sourceLayer]
		if !constrained {
			return nil
		}
		content, err := fs.ReadFile(root, filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, content, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", filename, err)
		}
		for _, specification := range parsed.Imports {
			targetLayer, err := importedInternalLayer(specification)
			if err != nil {
				return fmt.Errorf("parse import in %s: %w", filename, err)
			}
			if targetLayer == "" {
				continue
			}
			if _, ok := allowed[targetLayer]; !ok {
				violations = append(violations, fmt.Sprintf("%s: %s imports %s", filename, sourceLayer, targetLayer))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func importedInternalLayer(specification *ast.ImportSpec) (string, error) {
	importPath, err := strconv.Unquote(specification.Path.Value)
	if err != nil {
		return "", err
	}
	marker := strings.Index(importPath, internalImportMarker)
	if marker < 0 {
		return "", nil
	}
	remainder := strings.TrimPrefix(importPath[marker+len(internalImportMarker):], "/")
	if remainder == "" {
		return "", nil
	}
	return strings.SplitN(remainder, "/", 2)[0], nil
}

func TestProductionModuleImportFixtureRejectsCrossModuleDependency(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	writeFixture("internal/modules/orders/service.go", `package orders
import (
	"example.test/product/internal/modules/iam/session"
	"example.test/product/internal/modules/orders/transport"
)
`)
	writeFixture("internal/modules/orders/service_test.go", `package orders
import "example.test/product/internal/modules/iam/authorization"
`)

	violations, err := productionModuleImportViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect fixture: %v", err)
	}
	want := []string{"internal/modules/orders/service.go: orders imports iam"}
	if fmt.Sprint(violations) != fmt.Sprint(want) {
		t.Fatalf("violations = %v, want %v", violations, want)
	}
}

func TestProductionModulesOnlyAccessOwnedTables(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	violations, err := productionModuleTableViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect production module table access: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("production modules must access only tables declared by their own migrations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestProductionModuleTableFixtureRejectsForeignOwnership(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	writeFixture("internal/modules/orders/migrations/sqlite/001_orders.sql", `CREATE TABLE orders_items (id TEXT PRIMARY KEY);`)
	writeFixture("internal/modules/iam/migrations/sqlite/001_iam.sql", `CREATE TABLE IF NOT EXISTS "iam_accounts" (id TEXT PRIMARY KEY);`)
	writeFixture("internal/modules/catalog/migrations/sqlite/001_catalog.sql", `CREATE TABLE products (id TEXT PRIMARY KEY);`)
	writeFixture("internal/modules/orders/repository.go", "package orders\nconst own = `SELECT id FROM orders_items`\nconst foreign = \"SELECT id FROM \" + \"IAM_\" + \"ACCOUNTS\"\n")
	writeFixture("internal/modules/orders/repository_test.go", "package orders\nconst ignored = `SELECT id FROM iam_accounts`\n")
	writeFixture("internal/modules/demo/product.go", "package demo\nconst productName = `products`\n")

	violations, err := productionModuleTableViolations(os.DirFS(root))
	if err != nil {
		t.Fatalf("inspect fixture: %v", err)
	}
	want := []string{"internal/modules/orders/repository.go: orders accesses iam table iam_accounts"}
	if fmt.Sprint(violations) != fmt.Sprint(want) {
		t.Fatalf("violations = %v, want %v", violations, want)
	}
}

func TestModuleTableOwnershipRejectsConflictingModules(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"internal/modules/orders/migrations/sqlite/001_orders.sql",
		"internal/modules/iam/migrations/postgres/001_iam.sql",
	} {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(filename, []byte(`CREATE TABLE shared_records (id TEXT PRIMARY KEY);`), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	_, err := moduleTableOwnership(os.DirFS(root))
	if err == nil || !strings.Contains(err.Error(), "table shared_records is owned by both iam and orders") {
		t.Fatalf("conflicting ownership error = %v", err)
	}
}

func TestReferencedSQLTablesReadsTablePositionsOnly(t *testing.T) {
	tests := map[string]struct {
		value string
		want  []string
	}{
		"ordinary product text": {value: "products"},
		"quoted value":          {value: "SELECT 'FROM iam_accounts' AS message FROM orders_items", want: []string{"orders_items"}},
		"qualified identifier":  {value: `SELECT * FROM "public"."iam_accounts"`, want: []string{"iam_accounts"}},
		"update only":           {value: "UPDATE ONLY iam_accounts SET enabled = true", want: []string{"iam_accounts"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := referencedSQLTables(test.value); fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("referenced tables = %v, want %v", got, test.want)
			}
		})
	}
}

func productionModuleTableViolations(root fs.FS) ([]string, error) {
	owners, err := moduleTableOwnership(root)
	if err != nil {
		return nil, err
	}
	var violations []string
	seen := map[string]struct{}{}
	err = fs.WalkDir(root, "internal/modules", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filename) != ".go" || strings.HasSuffix(filename, "_test.go") {
			return nil
		}
		parts := strings.Split(filename, "/")
		if len(parts) < 4 {
			return nil
		}
		sourceModule := parts[2]
		content, err := fs.ReadFile(root, filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, content, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", filename, err)
		}
		var inspectErr error
		ast.Inspect(parsed, func(node ast.Node) bool {
			expression, ok := node.(ast.Expr)
			if !ok || inspectErr != nil {
				return inspectErr == nil
			}
			value, static, err := staticStringValue(expression)
			if err != nil {
				inspectErr = fmt.Errorf("parse string in %s: %w", filename, err)
				return false
			}
			if !static {
				return true
			}
			for _, table := range referencedSQLTables(value) {
				owner, exists := owners[table]
				if !exists || owner == sourceModule {
					continue
				}
				violation := fmt.Sprintf("%s: %s accesses %s table %s", filename, sourceModule, owner, table)
				if _, exists := seen[violation]; !exists {
					seen[violation] = struct{}{}
					violations = append(violations, violation)
				}
			}
			return true
		})
		return inspectErr
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func referencedSQLTables(value string) []string {
	tokens := sqlTokens(value)
	if len(tokens) == 0 {
		return nil
	}
	startsSQL := map[string]struct{}{
		"alter": {}, "create": {}, "delete": {}, "drop": {}, "insert": {},
		"merge": {}, "select": {}, "truncate": {}, "update": {}, "with": {},
	}
	if _, ok := startsSQL[tokens[0]]; !ok {
		return nil
	}

	seen := map[string]struct{}{}
	var tables []string
	for index, token := range tokens {
		isReference := token == "from" || token == "into" || token == "join" || token == "truncate" || token == "update"
		if token == "table" && index > 0 {
			previous := tokens[index-1]
			isReference = previous == "alter" || previous == "create" || previous == "drop"
		}
		if !isReference {
			continue
		}
		next := index + 1
		if next < len(tokens) && tokens[next] == "only" {
			next++
		}
		if next >= len(tokens) || !sqlIdentifierPattern.MatchString(tokens[next]) {
			continue
		}
		table := tokens[next]
		if next+2 < len(tokens) && tokens[next+1] == "." && sqlIdentifierPattern.MatchString(tokens[next+2]) {
			table = tokens[next+2]
		}
		if _, exists := seen[table]; !exists {
			seen[table] = struct{}{}
			tables = append(tables, table)
		}
	}
	return tables
}

func sqlTokens(value string) []string {
	var tokens []string
	for index := 0; index < len(value); {
		switch {
		case value[index] == '-' && index+1 < len(value) && value[index+1] == '-':
			index += 2
			for index < len(value) && value[index] != '\n' {
				index++
			}
		case value[index] == '/' && index+1 < len(value) && value[index+1] == '*':
			index += 2
			for index+1 < len(value) && !(value[index] == '*' && value[index+1] == '/') {
				index++
			}
			if index+1 < len(value) {
				index += 2
			}
		case value[index] == '\'':
			index, _ = skipSQLQuoted(value, index, '\'')
		case value[index] == '"' || value[index] == '`':
			quote := value[index]
			end, closed := skipSQLQuoted(value, index, quote)
			if closed {
				identifier := strings.ToLower(value[index+1 : end-1])
				if sqlIdentifierPattern.MatchString(identifier) {
					tokens = append(tokens, identifier)
				}
			}
			index = end
		case value[index] == '.':
			tokens = append(tokens, ".")
			index++
		case isSQLIdentifierStart(value[index]):
			end := index + 1
			for end < len(value) && isSQLIdentifierPart(value[end]) {
				end++
			}
			tokens = append(tokens, strings.ToLower(value[index:end]))
			index = end
		default:
			index++
		}
	}
	return tokens
}

func skipSQLQuoted(value string, start int, quote byte) (int, bool) {
	for index := start + 1; index < len(value); index++ {
		if value[index] != quote {
			continue
		}
		if index+1 < len(value) && value[index+1] == quote {
			index++
			continue
		}
		return index + 1, true
	}
	return len(value), false
}

func isSQLIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isSQLIdentifierPart(value byte) bool {
	return isSQLIdentifierStart(value) || value >= '0' && value <= '9'
}

func staticStringValue(expression ast.Expr) (string, bool, error) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false, nil
		}
		result, err := strconv.Unquote(value.Value)
		return result, true, err
	case *ast.ParenExpr:
		return staticStringValue(value.X)
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false, nil
		}
		left, leftStatic, err := staticStringValue(value.X)
		if err != nil || !leftStatic {
			return "", leftStatic, err
		}
		right, rightStatic, err := staticStringValue(value.Y)
		if err != nil || !rightStatic {
			return "", rightStatic, err
		}
		return left + right, true, nil
	default:
		return "", false, nil
	}
}

func moduleTableOwnership(root fs.FS) (map[string]string, error) {
	owners := map[string]string{}
	err := fs.WalkDir(root, "internal/modules", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filename) != ".sql" || !strings.Contains(filename, "/migrations/") {
			return nil
		}
		parts := strings.Split(filename, "/")
		if len(parts) < 5 {
			return nil
		}
		module := parts[2]
		content, err := fs.ReadFile(root, filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		for _, match := range createTablePattern.FindAllSubmatch(content, -1) {
			table := strings.ToLower(string(match[1]))
			if owner, exists := owners[table]; exists && owner != module {
				modules := []string{owner, module}
				sort.Strings(modules)
				return fmt.Errorf("table %s is owned by both %s and %s", table, modules[0], modules[1])
			}
			owners[table] = module
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return owners, nil
}

func productionModuleImportViolations(root fs.FS) ([]string, error) {
	var violations []string
	seen := map[string]struct{}{}
	err := fs.WalkDir(root, "internal/modules", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filename) != ".go" || strings.HasSuffix(filename, "_test.go") {
			return nil
		}
		parts := strings.Split(filename, "/")
		if len(parts) < 4 {
			return nil
		}
		sourceModule := parts[2]
		content, err := fs.ReadFile(root, filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, content, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", filename, err)
		}
		for _, specification := range parsed.Imports {
			target, err := importModule(specification)
			if err != nil {
				return fmt.Errorf("parse import in %s: %w", filename, err)
			}
			if target == "" || target == sourceModule {
				continue
			}
			violation := fmt.Sprintf("%s: %s imports %s", filename, sourceModule, target)
			if _, exists := seen[violation]; !exists {
				seen[violation] = struct{}{}
				violations = append(violations, violation)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func importModule(specification *ast.ImportSpec) (string, error) {
	importPath, err := strconv.Unquote(specification.Path.Value)
	if err != nil {
		return "", err
	}
	marker := strings.Index(importPath, moduleImportMarker)
	if marker < 0 {
		return "", nil
	}
	remainder := strings.TrimPrefix(importPath[marker+len(moduleImportMarker):], "/")
	if remainder == "" {
		return "", nil
	}
	return strings.SplitN(remainder, "/", 2)[0], nil
}
