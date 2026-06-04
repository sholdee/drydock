package pluginpolicy

import (
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"unicode"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v4"
)

func parseContainerPlugin(fields map[string]*yaml.Node, path, pointer string) (Plugin, error) {
	if err := rejectUnknownFields(fields, containerPluginAllowedFields(), pointer); err != nil {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	optional, err := parsePluginOptionalFields(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	runtime, err := parseContainerRuntime(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	allowMutableImageTag, err := parseContainerAllowMutableImageTag(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	image, err := parseContainerImage(fields, allowMutableImageTag, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	network, err := parseContainerNetwork(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	cacheMounts, err := parseContainerCacheMounts(fields["cacheMounts"], path, pointer+".cacheMounts")
	if err != nil {
		return Plugin{}, err
	}
	lifecycle, err := parseExecLifecycleConfig(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	return Plugin{
		Engine:                 EngineContainer,
		Match:                  optional.matchPtr(),
		ConfigManagementPlugin: optional.seedPtr(),
		Container: &ContainerConfig{
			Runtime:              runtime,
			Image:                image,
			AllowMutableImageTag: allowMutableImageTag,
			Network:              network,
			CacheMounts:          cacheMounts,
			Lifecycle:            lifecycle,
		},
	}, nil
}

func containerPluginAllowedFields() map[string]bool {
	fields := execLifecycleAllowedFields()
	fields["engine"] = true
	fields["match"] = true
	fields["configManagementPlugin"] = true
	fields["runtime"] = true
	fields["image"] = true
	fields["allowMutableImageTag"] = true
	fields["network"] = true
	fields["cacheMounts"] = true
	return fields
}

func parseContainerRuntime(fields map[string]*yaml.Node, path, pointer string) (ContainerRuntime, error) {
	runtime := DefaultContainerRuntime
	if node := fields["runtime"]; node != nil {
		value, err := stringValue(node, pointer+".runtime")
		if err != nil {
			return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		runtime = ContainerRuntime(strings.TrimSpace(value))
	}
	switch runtime {
	case ContainerRuntimeDocker:
		return runtime, nil
	default:
		return "", fmt.Errorf("parse plugin policy %s: %s.runtime has unsupported container runtime %q", path, pointer, runtime)
	}
}

func parseContainerAllowMutableImageTag(fields map[string]*yaml.Node, path, pointer string) (bool, error) {
	if fields["allowMutableImageTag"] == nil {
		return false, nil
	}
	allow, err := boolValue(fields["allowMutableImageTag"], pointer+".allowMutableImageTag")
	if err != nil {
		return false, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	return allow, nil
}

func parseContainerImage(fields map[string]*yaml.Node, allowMutableImageTag bool, path, pointer string) (string, error) {
	value, err := requiredString(fields, "image", pointer+".image")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	image := strings.TrimSpace(value)
	if image == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s.image must not be empty", path, pointer)
	}
	ref, err := reference.Parse(image)
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %s.image %q is invalid: %w", path, pointer, image, err)
	}
	named, ok := ref.(reference.Named)
	if !ok {
		return "", fmt.Errorf("parse plugin policy %s: %s.image %q must be a named image reference", path, pointer, image)
	}
	if reference.Domain(named) == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s.image %q must be fully qualified with a registry host", path, pointer, image)
	}
	_, hasDigest := ref.(reference.Digested)
	if hasDigest {
		return image, nil
	}
	if _, hasTag := ref.(reference.Tagged); !hasTag {
		return "", fmt.Errorf("parse plugin policy %s: %s.image %q must include a digest or tag", path, pointer, image)
	}
	if !allowMutableImageTag {
		return "", fmt.Errorf("parse plugin policy %s: %s.image digest is required unless allowMutableImageTag is true", path, pointer)
	}
	return image, nil
}

func parseContainerNetwork(fields map[string]*yaml.Node, path, pointer string) (ContainerNetwork, error) {
	network := DefaultContainerNetwork
	if node := fields["network"]; node != nil {
		value, err := stringValue(node, pointer+".network")
		if err != nil {
			return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		network = ContainerNetwork(strings.TrimSpace(value))
	}
	switch network {
	case ContainerNetworkNone, ContainerNetworkDefault:
		return network, nil
	default:
		return "", fmt.Errorf("parse plugin policy %s: %s.network has unsupported container network %q", path, pointer, network)
	}
}

func parseContainerCacheMounts(node *yaml.Node, policyPath, pointer string) ([]ContainerCacheMount, error) {
	if node == nil || isNullNode(node) {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("parse plugin policy %s: %s must be a sequence", policyPath, pointer)
	}
	if len(node.Content) == 0 {
		return nil, fmt.Errorf("parse plugin policy %s: %s must not be empty", policyPath, pointer)
	}
	if len(node.Content) > maxContainerCacheMountCount {
		return nil, fmt.Errorf("parse plugin policy %s: %s must contain at most %d entries", policyPath, pointer, maxContainerCacheMountCount)
	}
	mounts := make([]ContainerCacheMount, 0, len(node.Content))
	seenNames := map[string]struct{}{}
	seenTargets := map[string]struct{}{}
	for index, child := range node.Content {
		itemPointer := fmt.Sprintf("%s[%d]", pointer, index)
		mount, err := parseContainerCacheMount(child, policyPath, itemPointer)
		if err != nil {
			return nil, err
		}
		if _, ok := seenNames[mount.Name]; ok {
			return nil, fmt.Errorf("parse plugin policy %s: %s.name duplicate cache mount name %q", policyPath, itemPointer, mount.Name)
		}
		seenNames[mount.Name] = struct{}{}
		if _, ok := seenTargets[mount.Target]; ok {
			return nil, fmt.Errorf("parse plugin policy %s: %s.target duplicate cache mount target %q", policyPath, itemPointer, mount.Target)
		}
		seenTargets[mount.Target] = struct{}{}
		mounts = append(mounts, mount)
	}
	if err := rejectOverlappingContainerCacheTargets(mounts, policyPath, pointer); err != nil {
		return nil, err
	}
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Name < mounts[j].Name
	})
	return mounts, nil
}

func parseContainerCacheMount(node *yaml.Node, policyPath, pointer string) (ContainerCacheMount, error) {
	if node.Kind != yaml.MappingNode {
		return ContainerCacheMount{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", policyPath, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return ContainerCacheMount{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"name": true, "target": true}, pointer); err != nil {
		return ContainerCacheMount{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name, err := parseContainerCacheMountName(fields, policyPath, pointer)
	if err != nil {
		return ContainerCacheMount{}, err
	}
	target, err := parseContainerCacheMountTarget(fields, policyPath, pointer)
	if err != nil {
		return ContainerCacheMount{}, err
	}
	return ContainerCacheMount{Name: name, Target: target}, nil
}

func parseContainerCacheMountName(fields map[string]*yaml.Node, policyPath, pointer string) (string, error) {
	value, err := requiredString(fields, "name", pointer+".name")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name := strings.TrimSpace(value)
	if name != value {
		return "", fmt.Errorf("parse plugin policy %s: %s.name must not contain leading or trailing whitespace", policyPath, pointer)
	}
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("parse plugin policy %s: %s.name must not be empty, . or ..", policyPath, pointer)
	}
	if len(name) > 63 || !bootstrapEntrypointNamePattern.MatchString(name) || hasCommaOrControl(name) {
		return "", fmt.Errorf("parse plugin policy %s: %s.name %q must be a DNS-label-like cache name", policyPath, pointer, name)
	}
	return name, nil
}

func parseContainerCacheMountTarget(fields map[string]*yaml.Node, policyPath, pointer string) (string, error) {
	value, err := requiredString(fields, "target", pointer+".target")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	target := strings.TrimSpace(value)
	if target != value {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must not contain leading or trailing whitespace", policyPath, pointer)
	}
	if target == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must not be empty", policyPath, pointer)
	}
	if strings.Contains(target, "\\") {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must use Linux container paths", policyPath, pointer)
	}
	if hasCommaOrControl(target) {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must not contain commas or control characters", policyPath, pointer)
	}
	if !pathpkg.IsAbs(target) {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must be an absolute Linux container path", policyPath, pointer)
	}
	if hasDotDotPathComponent(target) {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must not contain .. path components", policyPath, pointer)
	}
	cleaned := pathpkg.Clean(target)
	if cleaned == "/work" || strings.HasPrefix(cleaned, "/work/") {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must not overlap the /work source mount", policyPath, pointer)
	}
	if cleaned == ContainerCacheTargetRoot || !strings.HasPrefix(cleaned, ContainerCacheTargetRoot+"/") {
		return "", fmt.Errorf("parse plugin policy %s: %s.target must be under %s without using the root itself", policyPath, pointer, ContainerCacheTargetRoot)
	}
	return cleaned, nil
}

func rejectOverlappingContainerCacheTargets(mounts []ContainerCacheMount, policyPath, pointer string) error {
	targets := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		targets = append(targets, mount.Target)
	}
	sort.Strings(targets)
	for index := 1; index < len(targets); index++ {
		previous := targets[index-1]
		current := targets[index]
		if current == previous || strings.HasPrefix(current, previous+"/") {
			return fmt.Errorf("parse plugin policy %s: %s contains overlapping cache mount targets %q and %q", policyPath, pointer, previous, current)
		}
	}
	return nil
}

func hasCommaOrControl(value string) bool {
	for _, char := range value {
		if char == ',' || unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func hasDotDotPathComponent(value string) bool {
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}
