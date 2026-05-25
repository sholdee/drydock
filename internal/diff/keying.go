package diff

import (
	"fmt"
	"sort"
	"strings"
)

func keyOf(doc Document) string {
	return strings.Join([]string{
		parentKind(doc.Parent),
		doc.Parent.Namespace,
		doc.Parent.Name,
		doc.Resource.Group,
		doc.Resource.Kind,
		doc.Resource.Namespace,
		doc.Resource.Name,
	}, "\x00")
}
func documentsByKey(docs []Document) map[string]Document {
	out := make(map[string]Document, len(docs))
	for _, doc := range docs {
		out[keyOf(doc)] = doc
	}
	return out
}
func sortedKeys(left, right map[string]Document) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func headerOf(doc Document) string {
	parts := []string{
		fmt.Sprintf("%s: %s", parentKind(doc.Parent), parentName(doc.Parent)),
		fmt.Sprintf("Source: %d", doc.Parent.SourceIndex),
	}
	if doc.Parent.SourceName != "" {
		parts = append(parts, fmt.Sprintf("name=%q", doc.Parent.SourceName))
	}
	if doc.Parent.SourcePath != "" {
		parts = append(parts, doc.Parent.SourcePath)
	}
	parts = append(parts, resourceName(doc.Resource))
	return strings.Join(parts, " ")
}
func parentKind(parent Parent) string {
	return "Application"
}
func parentName(parent Parent) string {
	if parent.Namespace == "" {
		return parent.Name
	}
	return parent.Namespace + "/" + parent.Name
}
func resourceName(resource Resource) string {
	kind := resource.Kind
	if resource.Group != "" {
		kind = resource.Group + "/" + kind
	}
	name := resource.Name
	if resource.Namespace != "" {
		name = resource.Namespace + "/" + name
	}
	return kind + ": " + name
}
