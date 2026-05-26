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

type moduleInfo struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Origin  struct {
		Hash string `json:"Hash"`
	} `json:"Origin"`
}

type renovateUpgrade struct {
	DepName     string `json:"depName"`
	PackageName string `json:"packageName"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "sync Argo CD GitOps Engine: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	sync, err := shouldSyncFromRenovateData(os.Getenv("RENOVATE_POST_UPGRADE_COMMAND_DATA_FILE"))
	if err != nil {
		return err
	}
	if !sync {
		fmt.Println("Argo CD was not upgraded; skipping GitOps Engine sync")
		return nil
	}

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
	if err := runGo("mod", "tidy"); err != nil {
		return fmt.Errorf("tidy modules: %w", err)
	}

	fmt.Printf("Aligned %s replace to %s for %s %s\n", gitopsEngineModule, engine.Version, argoCDModule, argo.Version)
	return nil
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

func runGo(args ...string) error {
	cmd := exec.Command("go", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
