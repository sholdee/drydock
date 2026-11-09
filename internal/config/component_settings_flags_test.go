package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"k8s.io/client-go/tools/clientcmd"
)

// flagSource is one Go file of the Argo CD module whose flag registrations
// a program gets, limited to funcs when it names any.
type flagSource struct {
	file  string
	funcs []string
}

// The files that register the flags of each program in Argo CD v3.6.1:
// the command, the cache flags (util/cache AddCacheFlagsToCmd and the
// reposerver and appstate caches), the TLS flags of the repo-server, the
// repo-server client TLS flags and the kubeconfig flag of the controllers.
// The client-go override flags of the controllers come from
// clientcmd.BindOverrideFlags itself (kubectlOverrideFlags).
var componentFlagSources = map[ArgoCDComponent][]flagSource{
	RepoServerComponent: {
		{file: "cmd/argocd-repo-server/commands/argocd_repo_server.go"},
		{file: "util/cache/cache.go", funcs: []string{"AddCacheFlagsToCmd"}},
		{file: "reposerver/cache/cache.go", funcs: []string{"AddCacheFlagsToCmd"}},
		{file: "util/tls/tls.go", funcs: []string{"AddTLSFlagsToCmdWithPrefix"}},
	},
	ApplicationControllerComponent: {
		{file: "cmd/argocd-application-controller/commands/argocd_application_controller.go"},
		{file: "util/cache/cache.go", funcs: []string{"AddCacheFlagsToCmd"}},
		{file: "util/cache/appstate/cache.go", funcs: []string{"AddCacheFlagsToCmd"}},
		{file: "util/tls/tls.go", funcs: []string{"AddClientTLSFlagsToCmdWithPrefix"}},
		{file: "util/cli/cli.go", funcs: []string{"AddKubectlFlagsToSet"}},
	},
	ApplicationSetControllerComponent: {
		{file: "cmd/argocd-applicationset-controller/commands/applicationset_controller.go"},
		{file: "util/tls/tls.go", funcs: []string{"AddClientTLSFlagsToCmdWithPrefix"}},
		{file: "util/cli/cli.go", funcs: []string{"AddKubectlFlagsToSet"}},
	},
}

// flagMethod matches the pflag registration methods, such as BoolVar,
// StringSliceVarP, or DurationVar, and the util/cli helpers such as
// BoundedFloat64Var.
var flagMethod = regexp.MustCompile(`^(Bool|String|StringSlice|StringArray|StringToString|Int|Int32|Int64|Uint|Uint32|Uint64|Float32|Float64|BoundedFloat64|Duration|Count|IP|IPSlice)(Var)?P?$`)

// The flag tables of drydock match the flags that Argo CD v3.6.1 registers
// for each program, with the same boolean or value kind. The test reads the
// Go source of the module in the module cache, so an Argo CD bump that adds,
// removes, or changes a flag fails it until the tables follow.
func TestComponentFlagTablesMatchArgoCD(t *testing.T) {
	dir := moduleDir(t, "github.com/argoproj/argo-cd/v3")
	for component, sources := range componentFlagSources {
		t.Run(string(component), func(t *testing.T) {
			want := map[string]bool{}
			for _, source := range sources {
				for name, isBool := range registeredFlags(t, filepath.Join(dir, filepath.FromSlash(source.file)), source.funcs) {
					want[name] = isBool
				}
			}
			if component != RepoServerComponent {
				for name, isBool := range kubectlOverrideFlags() {
					want[name] = isBool
				}
			}
			got := drydockComponentFlags(component)
			if diff := flagTableDiff(want, got); diff != "" {
				t.Fatalf("flag tables of %s differ from Argo CD v3.6.1 (name: Argo CD kind, drydock kind):\n%s", component, diff)
			}
		})
	}
}

func moduleDir(t *testing.T, module string) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	dir := strings.TrimSpace(string(out))
	if err != nil || dir == "" {
		t.Skipf("module %s is not in the module cache: %v", module, err)
	}
	return dir
}

// registeredFlags returns the flags that the calls in file register, in
// the funcs it names, or in the whole file without any. The value tells a
// boolean flag.
func registeredFlags(t *testing.T, file string, funcs []string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	consts := stringConstants(parsed)
	out := map[string]bool{}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (len(funcs) > 0 && !slices.Contains(funcs, fn.Name.Name)) {
			continue
		}
		ast.Inspect(fn, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !flagMethod.MatchString(selector.Sel.Name) || !flagSetReceiver(selector.X) {
				return true
			}
			for _, arg := range call.Args {
				if name, ok := flagName(arg, consts); ok {
					out[name] = strings.HasPrefix(selector.Sel.Name, "Bool")
					break
				}
			}
			return true
		})
	}
	return out
}

// flagSetReceiver reports a call on cmd.Flags(), cmd.PersistentFlags(), a
// FlagSet variable named flags or fs, or the util/cli package.
func flagSetReceiver(expr ast.Expr) bool {
	switch receiver := expr.(type) {
	case *ast.CallExpr:
		if selector, ok := receiver.Fun.(*ast.SelectorExpr); ok {
			return selector.Sel.Name == "Flags" || selector.Sel.Name == "PersistentFlags"
		}
	case *ast.Ident:
		return receiver.Name == "flags" || receiver.Name == "fs" || receiver.Name == "cli"
	}
	return false
}

// flagName returns the flag name of an argument: a string literal, the
// literal after a prefix such as opt.FlagPrefix + "redis", or a string
// constant of the file.
func flagName(expr ast.Expr, consts map[string]string) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		name, err := strconv.Unquote(value.Value)
		return name, err == nil
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			return flagName(value.Y, consts)
		}
	case *ast.Ident:
		name, ok := consts[value.Name]
		return name, ok
	}
	return "", false
}

func stringConstants(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if i < len(valueSpec.Values) {
					if literal, ok := valueSpec.Values[i].(*ast.BasicLit); ok && literal.Kind == token.STRING {
						if text, err := strconv.Unquote(literal.Value); err == nil {
							out[name.Name] = text
						}
					}
				}
			}
		}
	}
	return out
}

// kubectlOverrideFlags returns the flags that util/cli.AddKubectlFlagsToSet
// binds through clientcmd.BindOverrideFlags.
func kubectlOverrideFlags() map[string]bool {
	flags := pflag.NewFlagSet("kubectl", pflag.ContinueOnError)
	overrides := clientcmd.ConfigOverrides{}
	clientcmd.BindOverrideFlags(&overrides, flags, clientcmd.RecommendedConfigOverrideFlags(""))
	out := map[string]bool{}
	flags.VisitAll(func(flag *pflag.Flag) {
		out[flag.Name] = flag.Value.Type() == "bool"
	})
	return out
}

// drydockComponentFlags returns the flags that the tables of drydock know
// for component, without the help flag that cobra adds.
func drydockComponentFlags(component ArgoCDComponent) map[string]bool {
	out := map[string]bool{}
	for _, name := range componentValueFlags[component] {
		out[name] = false
	}
	for _, name := range componentBoolFlags[component] {
		out[name] = true
	}
	for _, setting := range componentSettings[component] {
		if setting.Flag != "" {
			out[setting.Flag] = setting.kind == componentFlagBool
		}
	}
	delete(out, "help")
	return out
}

func flagTableDiff(want, got map[string]bool) string {
	kind := func(flags map[string]bool, name string) string {
		isBool, ok := flags[name]
		switch {
		case !ok:
			return "missing"
		case isBool:
			return "bool"
		default:
			return "value"
		}
	}
	names := map[string]struct{}{}
	for name := range want {
		names[name] = struct{}{}
	}
	for name := range got {
		names[name] = struct{}{}
	}
	var lines []string
	for name := range names {
		if wantKind, gotKind := kind(want, name), kind(got, name); wantKind != gotKind {
			lines = append(lines, name+": "+wantKind+", "+gotKind)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
