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
	gitopsEngineModule = "github.com/argoproj/argo-cd/gitops-engine"
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
	Origin  struct {
		Hash string `json:"Hash"`
	} `json:"Origin"`
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
	if strings.TrimSpace(argoVersion.Origin.Hash) == "" {
		return fmt.Errorf("selected Argo CD version %s did not report an origin hash", argo.Version)
	}
	if strings.TrimSpace(argoVersion.GoMod) == "" {
		return fmt.Errorf("selected Argo CD version %s did not report a go.mod path", argo.Version)
	}

	engine, err := goListModule(gitopsEngineModule + "@" + argoVersion.Origin.Hash)
	if err != nil {
		return fmt.Errorf("resolve GitOps Engine at Argo CD commit %s: %w", argoVersion.Origin.Hash, err)
	}
	if strings.TrimSpace(engine.Version) == "" {
		return fmt.Errorf("resolved GitOps Engine at Argo CD commit %s did not report a module version", argoVersion.Origin.Hash)
	}

	if err := runGo("mod", "edit", "-replace="+gitopsEngineModule+"="+gitopsEngineModule+"@"+engine.Version); err != nil {
		return fmt.Errorf("update GitOps Engine replace: %w", err)
	}

	argoGoMod, err := readGoMod(argoVersion.GoMod)
	if err != nil {
		return fmt.Errorf("read Argo CD go.mod: %w", err)
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
