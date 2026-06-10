package pluginonboarding

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v3"
)

func collectEmbeddedConfigManagementPlugins(root string) (map[string]config.ConfigManagementPlugin, error) {
	out := map[string]config.ConfigManagementPlugin{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		plugins, err := embeddedConfigManagementPluginsFromFile(filepath.ToSlash(rel), path)
		if err != nil {
			return err
		}
		for name, plugin := range plugins {
			if _, exists := out[name]; !exists {
				out[name] = plugin
			}
		}
		return nil
	})
	return out, err
}

func embeddedConfigManagementPluginsFromFile(relPath, path string) (map[string]config.ConfigManagementPlugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	out := map[string]config.ConfigManagementPlugin{}
	for index := 0; ; index++ {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, nil
		}
		for _, plugin := range embeddedConfigManagementPluginsFromNode(&node, relPath, fmt.Sprintf("doc[%d]", index)) {
			name := plugin.EffectiveName()
			if name == "" {
				continue
			}
			if _, exists := out[name]; !exists {
				out[name] = plugin
			}
		}
	}
	return out, nil
}

func embeddedConfigManagementPluginsFromNode(node *yaml.Node, path, pointer string) []config.ConfigManagementPlugin {
	if node == nil {
		return nil
	}
	var out []config.ConfigManagementPlugin
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return embeddedConfigManagementPluginsFromNode(node.Content[0], path, pointer)
	}
	if plugin, ok := configManagementPluginFromMappingNode(node, path, pointer); ok {
		out = append(out, plugin)
	}
	if node.Kind == yaml.ScalarNode && looksLikeEmbeddedConfigManagementPluginYAML(node.Value) {
		out = append(out, configManagementPluginsFromEmbeddedYAML([]byte(node.Value), path, pointer)...)
	}
	for index, child := range node.Content {
		out = append(out, embeddedConfigManagementPluginsFromNode(child, path, fmt.Sprintf("%s[%d]", pointer, index))...)
	}
	return out
}

func configManagementPluginFromMappingNode(node *yaml.Node, path, pointer string) (config.ConfigManagementPlugin, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return config.ConfigManagementPlugin{}, false
	}
	var doc embeddedConfigManagementPluginDocument
	if err := node.Decode(&doc); err != nil || doc.Kind != "ConfigManagementPlugin" {
		return config.ConfigManagementPlugin{}, false
	}
	return embeddedConfigManagementPluginFromDocument(doc, path, pointer), true
}

func configManagementPluginsFromEmbeddedYAML(data []byte, path, pointer string) []config.ConfigManagementPlugin {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var out []config.ConfigManagementPlugin
	for index := 0; ; index++ {
		var doc embeddedConfigManagementPluginDocument
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out
		}
		if doc.Kind != "ConfigManagementPlugin" {
			continue
		}
		out = append(out, embeddedConfigManagementPluginFromDocument(doc, path, fmt.Sprintf("%s#%d", pointer, index)))
	}
	return out
}

func embeddedConfigManagementPluginFromDocument(doc embeddedConfigManagementPluginDocument, path, pointer string) config.ConfigManagementPlugin {
	plugin := config.ConfigManagementPlugin{
		Name:            strings.TrimSpace(doc.Metadata.Name),
		Version:         strings.TrimSpace(doc.Spec.Version),
		GenerateCommand: cleanArgv(doc.Spec.Generate.Command),
		GenerateArgs:    cleanArgv(doc.Spec.Generate.Args),
		HasInit:         len(cleanArgv(doc.Spec.Init.Command)) > 0 || len(cleanArgv(doc.Spec.Init.Args)) > 0,
		Discover: config.ConfigManagementPluginDiscovery{
			FileName:    strings.TrimSpace(doc.Spec.Discover.FileName),
			FindGlob:    strings.TrimSpace(doc.Spec.Discover.Find.Glob),
			FindCommand: cleanArgv(doc.Spec.Discover.Find.Command),
			FindArgs:    cleanArgv(doc.Spec.Discover.Find.Args),
		},
		Provenance: diagnostic.Provenance{Path: path, Pointer: pointer},
	}
	return plugin
}

func looksLikeEmbeddedConfigManagementPluginYAML(value string) bool {
	return strings.Contains(value, "ConfigManagementPlugin")
}

type embeddedConfigManagementPluginDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec embeddedConfigManagementPluginSpec `yaml:"spec"`
}

type embeddedConfigManagementPluginSpec struct {
	Version  string                           `yaml:"version"`
	Init     embeddedConfigManagementCommand  `yaml:"init"`
	Generate embeddedConfigManagementCommand  `yaml:"generate"`
	Discover embeddedConfigManagementDiscover `yaml:"discover"`
}

type embeddedConfigManagementCommand struct {
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
}

type embeddedConfigManagementDiscover struct {
	FileName string `yaml:"fileName"`
	Find     struct {
		Glob    string   `yaml:"glob"`
		Command []string `yaml:"command"`
		Args    []string `yaml:"args"`
	} `yaml:"find"`
}
