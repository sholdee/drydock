package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const defaultPluginPolicyPath = ".drydock/plugins.yaml"

func ensureBuildPluginPolicy(ctx context.Context, request BuildRequest, root string) (BuildRequest, []diagnostic.Diagnostic, func(), error) {
	if err := validatePluginPolicyOptions(request.PluginOptions); err != nil {
		diag := pluginPolicyInvalidDiagnostic(err)
		return request, []diagnostic.Diagnostic{diag}, func() {}, err
	}
	if request.pluginPolicyLoaded {
		return request, nil, func() {}, nil
	}
	if request.DisablePluginPolicy {
		request.pluginPolicyLoaded = true
		request.pluginPolicyFingerprint = pluginpolicy.NoPolicyFingerprint
		request.pluginPolicyExecTrusted = false
		return request, nil, func() {}, nil
	}

	policyRoot := root
	cleanup := func() {}
	requirePolicy := false
	execTrusted := false
	if strings.TrimSpace(request.PluginPolicyRef) != "" {
		requirePolicy = true
		execTrusted = true
		repo := strings.TrimSpace(request.PluginPolicyRepo)
		if repo == "" {
			repo = root
		}
		result, err := gitref.Snapshot(ctx, gitref.Request{
			Repo:           repo,
			Ref:            request.PluginPolicyRef,
			ForbiddenRoots: compactStrings(root, repo),
		})
		if err != nil {
			err = fmt.Errorf("load plugin policy ref %q: %w", request.PluginPolicyRef, err)
			diag := pluginPolicyInvalidDiagnostic(err)
			return request, []diagnostic.Diagnostic{diag}, cleanup, err
		}
		policyRoot = result.Path
		cleanup = func() { _ = result.Cleanup() }
	}

	return loadPluginPolicyFromRoot(request, policyRoot, requirePolicy, execTrusted, cleanup)
}

func ensureDiffPluginPolicy(ctx context.Context, request DiffRequest) (DiffRequest, []diagnostic.Diagnostic, func(), error) {
	if err := validatePluginPolicyOptions(request.PluginOptions); err != nil {
		diag := pluginPolicyInvalidDiagnostic(err)
		return request, []diagnostic.Diagnostic{diag}, func() {}, err
	}
	if request.pluginPolicyLoaded {
		return request, nil, func() {}, nil
	}
	if request.DisablePluginPolicy {
		request.pluginPolicyLoaded = true
		request.pluginPolicyFingerprint = pluginpolicy.NoPolicyFingerprint
		request.pluginPolicyExecTrusted = false
		return request, nil, func() {}, nil
	}

	policyRoot := request.LeftPath
	cleanup := func() {}
	requirePolicy := false
	execTrusted := diffPluginPolicyExecTrusted(request)
	if strings.TrimSpace(request.PluginPolicyRef) != "" {
		requirePolicy = true
		execTrusted = true
		repo := strings.TrimSpace(request.PluginPolicyRepo)
		if repo == "" {
			repo = strings.TrimSpace(request.Repo)
		}
		if repo == "" {
			repo = request.LeftPath
		}
		result, err := gitref.Snapshot(ctx, gitref.Request{
			Repo:           repo,
			Ref:            request.PluginPolicyRef,
			ForbiddenRoots: compactStrings(request.LeftPath, request.RightPath, request.Repo, request.PluginPolicyRepo),
		})
		if err != nil {
			err = fmt.Errorf("load plugin policy ref %q: %w", request.PluginPolicyRef, err)
			diag := pluginPolicyInvalidDiagnostic(err)
			return request, []diagnostic.Diagnostic{diag}, cleanup, err
		}
		policyRoot = result.Path
		cleanup = func() { _ = result.Cleanup() }
	}

	buildRequest := BuildRequest{PluginOptions: request.PluginOptions}
	buildRequest, diags, loadCleanup, err := loadPluginPolicyFromRoot(buildRequest, policyRoot, requirePolicy, execTrusted, cleanup)
	request.PluginOptions = buildRequest.PluginOptions
	return request, diags, loadCleanup, err
}

func loadPluginPolicyFromRoot(request BuildRequest, root string, requirePolicy bool, execTrusted bool, cleanup func()) (BuildRequest, []diagnostic.Diagnostic, func(), error) {
	policy, fingerprint, ok, err := loadPluginPolicyFile(root, request.PluginOptions, requirePolicy)
	if err != nil {
		diag := pluginPolicyInvalidDiagnostic(err)
		return request, []diagnostic.Diagnostic{diag}, cleanup, err
	}
	request.pluginPolicyLoaded = true
	if ok {
		request.pluginPolicy = policy
		request.pluginPolicyFingerprint = fingerprint
		request.pluginPolicyExecTrusted = execTrusted
	} else {
		request.pluginPolicyFingerprint = pluginpolicy.NoPolicyFingerprint
		request.pluginPolicyExecTrusted = false
	}
	return request, nil, cleanup, nil
}

func diffPluginPolicyExecTrusted(request DiffRequest) bool {
	if strings.TrimSpace(request.RefOrig) != "" {
		return true
	}
	return pathsDiffer(request.LeftPath, request.RightPath)
}

func pathsDiffer(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = resolved
	}
	return filepath.Clean(leftAbs) != filepath.Clean(rightAbs)
}

func loadPluginPolicyFile(root string, options PluginOptions, requirePolicy bool) (pluginpolicy.Policy, string, bool, error) {
	relPath := strings.TrimSpace(options.PluginPolicyPath)
	explicit := options.PluginPolicyPathExplicit || relPath != ""
	if relPath == "" {
		if options.PluginPolicyPathExplicit {
			return pluginpolicy.Policy{}, "", false, fmt.Errorf("plugin policy path must not be empty")
		}
		relPath = defaultPluginPolicyPath
	}
	clean, err := cleanPluginPolicyPath(relPath)
	if err != nil {
		return pluginpolicy.Policy{}, "", false, err
	}
	target := filepath.Join(root, clean)
	inside, matchedRoot, err := pathsafety.IsInsideAny(target, []string{root})
	if err != nil {
		return pluginpolicy.Policy{}, "", false, fmt.Errorf("validate plugin policy path %q: %w", relPath, err)
	}
	if !inside {
		return pluginpolicy.Policy{}, "", false, fmt.Errorf("plugin policy path %q escapes policy root %q", relPath, matchedRoot)
	}

	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		if explicit || requirePolicy {
			return pluginpolicy.Policy{}, "", false, fmt.Errorf("plugin policy %q does not exist", relPath)
		}
		return pluginpolicy.Policy{}, pluginpolicy.NoPolicyFingerprint, false, nil
	}
	if err != nil {
		return pluginpolicy.Policy{}, "", false, fmt.Errorf("read plugin policy %q: %w", relPath, err)
	}
	if !info.Mode().IsRegular() {
		return pluginpolicy.Policy{}, "", false, fmt.Errorf("plugin policy %q must be a regular file", relPath)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return pluginpolicy.Policy{}, "", false, fmt.Errorf("read plugin policy %q: %w", relPath, err)
	}
	policy, err := pluginpolicy.Parse(relPath, data)
	if err != nil {
		return pluginpolicy.Policy{}, "", false, err
	}
	fingerprint, err := pluginpolicy.Fingerprint(policy)
	if err != nil {
		return pluginpolicy.Policy{}, "", false, err
	}
	return policy, fingerprint, true, nil
}

func cleanPluginPolicyPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("plugin policy path must not be empty")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == "." || pathsafety.RelEscapes(clean) {
		return "", fmt.Errorf("plugin policy path %q must be relative to the selected policy root", path)
	}
	return clean, nil
}

func validatePluginPolicyOptions(options PluginOptions) error {
	if options.DisablePluginPolicy {
		if options.PluginPolicyPathExplicit || strings.TrimSpace(options.PluginPolicyPath) != "" || strings.TrimSpace(options.PluginPolicyRef) != "" || strings.TrimSpace(options.PluginPolicyRepo) != "" {
			return fmt.Errorf("--disable-plugin-policy cannot be combined with plugin policy path, ref, or repo")
		}
	}
	if strings.TrimSpace(options.PluginPolicyRepo) != "" && strings.TrimSpace(options.PluginPolicyRef) == "" {
		return fmt.Errorf("plugin policy repo requires plugin policy ref")
	}
	if options.PluginPolicyPathExplicit && strings.TrimSpace(options.PluginPolicyPath) == "" {
		return fmt.Errorf("plugin policy path must not be empty")
	}
	return nil
}

func pluginPolicyInvalidDiagnostic(err error) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     diagnostic.CodePluginPolicyInvalid,
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  fmt.Sprintf("plugin policy invalid: %s", err),
	}
}

func compactStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
