package pluginpolicy

import (
	"fmt"
	"strings"

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
