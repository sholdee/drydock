package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	argoCDModule       = "github.com/argoproj/argo-cd/v3"
	gitopsEngineModule = "github.com/argoproj/argo-cd/gitops-engine/v3"
	// legacyGitopsEngineModule is the pre-v3.6 module path. Argo CD v3.5 and
	// earlier only reached consumers through a local replace, so drydock pinned
	// it by pseudo-version; drop any such leftover replace.
	legacyGitopsEngineModule = "github.com/argoproj/argo-cd/gitops-engine"
	// gojqModule parses and runs ignoreDifferences jqPathExpressions. It must
	// be the version Argo CD requires: a newer gojq parses or runs some
	// expressions differently, so drydock would hide fields where Argo CD
	// fails the comparison, or the reverse.
	gojqModule = "github.com/itchyny/gojq"
	// doublestarModule matches the file patterns of the Git generator with
	// applicationsetcontroller.enable.new.git.file.globbing, the OCI
	// generator, and Helm value file globs. It must be the version Argo CD
	// requires: releases change how patterns parse, such as braces in
	// {c,[a,b]}.yaml.
	doublestarModule = "github.com/bmatcuk/doublestar/v4"
)

var argoCompatibilityReplaceModules = []string{
	"k8s.io/api",
	"k8s.io/apiextensions-apiserver",
	"k8s.io/apimachinery",
	"k8s.io/apiserver",
	"k8s.io/cli-runtime",
	"k8s.io/client-go",
	"k8s.io/cloud-provider",
	"k8s.io/cluster-bootstrap",
	"k8s.io/code-generator",
	"k8s.io/component-base",
	"k8s.io/component-helpers",
	"k8s.io/controller-manager",
	"k8s.io/cri-api",
	"k8s.io/cri-client",
	"k8s.io/cri-streaming",
	"k8s.io/csi-translation-lib",
	"k8s.io/dynamic-resource-allocation",
	"k8s.io/endpointslice",
	"k8s.io/externaljwt",
	"k8s.io/kms",
	"k8s.io/kube-aggregator",
	"k8s.io/kube-controller-manager",
	"k8s.io/kube-proxy",
	"k8s.io/kube-scheduler",
	"k8s.io/kubectl",
	"k8s.io/kubelet",
	"k8s.io/metrics",
	"k8s.io/mount-utils",
	"k8s.io/pod-security-admission",
	"k8s.io/sample-apiserver",
	"sigs.k8s.io/controller-runtime",
}

type moduleInfo struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	GoMod   string `json:"GoMod"`
}

type goModFile struct {
	Require []goModRequire `json:"Require"`
	Replace []goModReplace `json:"Replace"`
}

type goModModule struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
}

type goModRequire struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
}

type goModReplace struct {
	Old goModModule `json:"Old"`
	New goModModule `json:"New"`
}

type renovateUpgrade struct {
	DepName     string `json:"depName"`
	PackageName string `json:"packageName"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "sync Argo CD compatibility stack: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	sync, err := shouldSyncFromRenovateData(os.Getenv("RENOVATE_POST_UPGRADE_COMMAND_DATA_FILE"))
	if err != nil {
		return err
	}
	if !sync {
		fmt.Println("Argo CD was not upgraded; skipping compatibility stack sync")
		return nil
	}

	return syncArgoCompatibilityStack()
}

func syncArgoCompatibilityStack() error {
	argo, err := goListModule(argoCDModule)
	if err != nil {
		return fmt.Errorf("read selected Argo CD module: %w", err)
	}
	if strings.TrimSpace(argo.Version) == "" {
		return errors.New("selected Argo CD module has no version")
	}

	argoVersion, err := goListModule(argoCDModule + "@" + argo.Version)
	if err != nil {
		return fmt.Errorf("resolve Argo CD version %s: %w", argo.Version, err)
	}
	if strings.TrimSpace(argoVersion.GoMod) == "" {
		return fmt.Errorf("selected Argo CD version %s did not report a go.mod path", argo.Version)
	}

	argoGoMod, err := readGoMod(argoVersion.GoMod)
	if err != nil {
		return fmt.Errorf("read Argo CD go.mod: %w", err)
	}

	// Since v3.6 GitOps Engine is a tagged module (gitops-engine/vX.Y.Z) that
	// Argo CD requires at its own release version. Argo CD still builds it from
	// a local replace, which consumers do not inherit, so require exactly the
	// version Argo CD declares and drop any replace left from older lines.
	engineVersion, err := gitopsEngineRequirement(argoGoMod)
	if err != nil {
		return err
	}
	if err := runGo("mod", "edit",
		"-dropreplace="+legacyGitopsEngineModule,
		"-droprequire="+legacyGitopsEngineModule,
		"-dropreplace="+gitopsEngineModule,
		"-require="+gitopsEngineModule+"@"+engineVersion,
	); err != nil {
		return fmt.Errorf("update GitOps Engine requirement: %w", err)
	}
	if err := requireGojqModules(argoGoMod); err != nil {
		return err
	}
	doublestarVersion, err := argoRequirement(argoGoMod, doublestarModule)
	if err != nil {
		return err
	}
	if err := runGo("mod", "edit", "-require="+doublestarModule+"@"+doublestarVersion); err != nil {
		return fmt.Errorf("update doublestar requirement: %w", err)
	}

	replacements, err := compatibilityReplacements(argoGoMod, argoCompatibilityReplaceModules)
	if err != nil {
		return err
	}
	for _, replacement := range replacements {
		if err := runGo("mod", "edit", "-replace="+replacement.Old.Path+"="+replacement.New.Path+"@"+replacement.New.Version); err != nil {
			return fmt.Errorf("update %s replace: %w", replacement.Old.Path, err)
		}
	}

	if err := runGo("mod", "tidy"); err != nil {
		return fmt.Errorf("tidy modules: %w", err)
	}

	fmt.Printf("Aligned Argo CD compatibility stack for %s %s\n", argoCDModule, argo.Version)
	return nil
}

func gitopsEngineRequirement(source goModFile) (string, error) {
	return argoRequirement(source, gitopsEngineModule)
}

// requireGojqModules requires gojq, and then every other module that the
// gojq package compiles into drydock (such as timefmt-go, which its date
// functions use), at the version the selected Argo CD requires. The module
// set is read after gojq moves, because a new gojq can change it.
func requireGojqModules(argoGoMod goModFile) error {
	gojqVersion, err := argoRequirement(argoGoMod, gojqModule)
	if err != nil {
		return err
	}
	if err := runGo("mod", "edit", "-require="+gojqModule+"@"+gojqVersion); err != nil {
		return fmt.Errorf("update gojq requirement: %w", err)
	}
	// -mod=mod lets go list read the go.mod that the edit above changed.
	modules, err := gojqModules("mod")
	if err != nil {
		return err
	}
	for _, module := range modules {
		version, err := argoRequirement(argoGoMod, module)
		if err != nil {
			return err
		}
		if err := runGo("mod", "edit", "-require="+module+"@"+version); err != nil {
			return fmt.Errorf("update %s requirement: %w", module, err)
		}
	}
	return nil
}

// gojqModules returns the modules whose packages the gojq package compiles
// into drydock, gojq included. Modules that only gojq's command-line tool
// uses are not part of drydock and are left out. mod is the -mod mode: the
// helper uses "mod" after it edits go.mod, and the test uses "readonly", so
// an out-of-date go.mod fails instead of being fixed.
func gojqModules(mod string) ([]string, error) {
	cmd := exec.Command("go", "list", "-mod="+mod, "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", gojqModule)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps %s: %w", gojqModule, err)
	}
	seen := map[string]struct{}{}
	var modules []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		modules = append(modules, line)
	}
	return modules, nil
}

// argoRequirement returns the version at which the selected Argo CD go.mod
// requires module.
func argoRequirement(source goModFile, module string) (string, error) {
	for _, requirement := range source.Require {
		if requirement.Path == module {
			version := strings.TrimSpace(requirement.Version)
			if version == "" {
				break
			}
			return version, nil
		}
	}
	return "", fmt.Errorf("selected Argo CD go.mod does not require %s at a version", module)
}

func compatibilityReplacements(source goModFile, modules []string) ([]goModReplace, error) {
	replaces := make(map[string]goModModule, len(source.Replace))
	for _, replacement := range source.Replace {
		replaces[replacement.Old.Path] = replacement.New
	}

	requires := make(map[string]string, len(source.Require))
	for _, requirement := range source.Require {
		requires[requirement.Path] = requirement.Version
	}

	replacements := make([]goModReplace, 0, len(modules))
	for _, module := range modules {
		if replacement, ok := replaces[module]; ok {
			if strings.TrimSpace(replacement.Path) == "" || strings.TrimSpace(replacement.Version) == "" {
				return nil, fmt.Errorf("selected Argo CD replace for %s does not point to a versioned module", module)
			}
			replacements = append(replacements, goModReplace{
				Old: goModModule{Path: module},
				New: replacement,
			})
			continue
		}

		if module == "sigs.k8s.io/controller-runtime" {
			version := strings.TrimSpace(requires[module])
			if version == "" {
				return nil, fmt.Errorf("selected Argo CD does not require %s and has no replace for it", module)
			}
			replacements = append(replacements, goModReplace{
				Old: goModModule{Path: module},
				New: goModModule{Path: module, Version: version},
			})
			continue
		}

		return nil, fmt.Errorf("selected Argo CD go.mod has no replace for compatibility pin %s", module)
	}

	return replacements, nil
}

func shouldSyncFromRenovateData(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return true, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read Renovate post-upgrade data file: %w", err)
	}

	var upgrades []renovateUpgrade
	if err := json.Unmarshal(data, &upgrades); err != nil {
		return false, fmt.Errorf("parse Renovate post-upgrade data file: %w", err)
	}
	for _, upgrade := range upgrades {
		if upgrade.DepName == argoCDModule || upgrade.PackageName == argoCDModule {
			return true, nil
		}
	}
	return false, nil
}

func goListModule(query string) (moduleInfo, error) {
	cmd := exec.Command("go", "list", "-m", "-json", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return moduleInfo{}, fmt.Errorf("go list -m -json %s: %w\n%s", query, err, strings.TrimSpace(string(output)))
	}

	var info moduleInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return moduleInfo{}, fmt.Errorf("parse go list output for %s: %w", query, err)
	}
	return info, nil
}

func readGoMod(path string) (goModFile, error) {
	cmd := exec.Command("go", "mod", "edit", "-json", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return goModFile{}, fmt.Errorf("go mod edit -json %s: %w\n%s", path, err, strings.TrimSpace(string(output)))
	}

	var mod goModFile
	if err := json.Unmarshal(output, &mod); err != nil {
		return goModFile{}, fmt.Errorf("parse go.mod JSON for %s: %w", path, err)
	}
	return mod, nil
}

func runGo(args ...string) error {
	cmd := exec.Command("go", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
