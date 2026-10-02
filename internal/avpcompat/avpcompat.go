// Package avpcompat mirrors where argocd-vault-plugin (AVP) substitutes
// placeholders, without any secret backend: every placeholder AVP would
// resolve becomes a deterministic marker derived from the placeholder's
// identity. Parity means drydock substitutes exactly where AVP substitutes
// and nowhere else, so the matching, span selection and traversal rules below
// are copied from AVP v1.18.1 pkg/kube/util.go and pkg/kube/template.go and
// cite the lines they mirror.
package avpcompat

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

const redactedPrefix = "drydock-redacted-"

// PathAnnotation is the AVP annotation that scopes generic <key> placeholders
// to a backend path.
const PathAnnotation = "avp.kubernetes.io/path"

// IgnoreAnnotation marks an object AVP emits verbatim: cmd/generate.go:93-102
// skips Replace() when its value parses true with strconv.ParseBool.
const IgnoreAnnotation = "avp.kubernetes.io/ignore"

// The three regular expressions are AVP's own, verbatim (pkg/kube/util.go:26-28).
//
//   - genericPlaceholder is used when the path annotation key is present
//     (util.go:113-115): (?U) makes `.*` lazy, so a match runs from the first
//     '<' on a line to the next '>' and never crosses a newline.
//   - specificPathPlaceholder is used when the annotation key is absent: it
//     only starts at a literal "<path:" and admits anything but '#' inside
//     the path and key parts, newlines and '>' included.
//   - indivPlaceholderSyntax is tested, unanchored, against the trimmed body of
//     every match (util.go:130-135) to decide between an inline path lookup and
//     a lookup in the annotation path's data.
var (
	genericPlaceholder      = regexp.MustCompile(`(?mU)<(.*)>`)
	specificPathPlaceholder = regexp.MustCompile(`(?mU)<path:([^#]+)#([^#]+)(?:#([^#]+))?>`)
	indivPlaceholderSyntax  = regexp.MustCompile(`(?mU)path:(?P<path>[^#]+?)#(?P<key>[^#]+?)(?:#(?P<version>.+?))??`)
)

var (
	indivPathIndex    = indivPlaceholderSyntax.SubexpIndex("path")
	indivKeyIndex     = indivPlaceholderSyntax.SubexpIndex("key")
	indivVersionIndex = indivPlaceholderSyntax.SubexpIndex("version")
)

// Options describes the manifest context AVP derives its replacement rules
// from (pkg/kube/template.go:31-60, 67-79).
type Options struct {
	// Kind is the manifest's top-level kind. AVP picks one replacer per object
	// from this string alone (template.go:70-77): "Secret" selects the
	// base64-aware replacer, everything else is processed as plain text.
	Kind string
	// PathAnnotationPresent reports whether metadata.annotations carries the
	// avp.kubernetes.io/path key at all. AVP selects the placeholder regex on
	// key presence, not on the value (util.go:113-115), so an empty annotation
	// still switches to the generic regex.
	PathAnnotationPresent bool
	// Path is the annotation value. Generic placeholders are only resolvable
	// when it is non-empty (template.go:33-48: an empty path fetches nothing,
	// so every generic lookup misses and AVP leaves the match unchanged).
	Path string
}

// ContainsPlaceholder reports whether value contains an inline path
// placeholder that AVP would substitute without a path annotation.
func ContainsPlaceholder(value string) bool {
	_, changed := ReplaceString(value)
	return changed
}

// ReplaceString replaces inline path placeholders with stable redacted values,
// as AVP does for a manifest without the path annotation.
func ReplaceString(value string) (string, bool) {
	return replaceString(value, Options{})
}

// ReplaceStringWithPath replaces placeholders as AVP does for a manifest whose
// path annotation is present with the given value: inline path placeholders
// and, when defaultPath is non-empty, generic <key> placeholders.
func ReplaceStringWithPath(value string, defaultPath string) (string, bool) {
	return replaceString(value, Options{PathAnnotationPresent: true, Path: defaultPath})
}

// ReplaceStringWithOptions replaces placeholders in a single string under the
// given manifest context.
func ReplaceStringWithOptions(value string, opts Options) (string, bool) {
	return replaceString(value, opts)
}

// ReplaceValue replaces inline path placeholders through decoded YAML/JSON
// values, as AVP does for a manifest without the path annotation.
func ReplaceValue(value any) (any, bool) {
	return replaceValue(value, Options{})
}

// ReplaceValueWithPath replaces placeholders through decoded YAML/JSON values
// as AVP does for a manifest whose path annotation is present with the given
// value.
func ReplaceValueWithPath(value any, defaultPath string) (any, bool) {
	return replaceValue(value, Options{PathAnnotationPresent: true, Path: defaultPath})
}

// ReplaceValueWithOptions replaces placeholders through decoded YAML/JSON
// values under the given manifest context. The input is never mutated; the
// returned value shares unchanged subtrees with it.
func ReplaceValueWithOptions(value any, opts Options) (any, bool) {
	return replaceValue(value, opts)
}

// replaceString is the per-string base case. For kind Secret it mirrors
// secretReplacement (util.go:217-227): every string in the object is first
// tried as standard, padded base64; when that decodes and the decoded bytes
// contain a generic <...> placeholder on one line (always the generic regex,
// whatever the annotation), the replacement runs on the decoded text and the
// result is re-encoded. AVP therefore canonicalizes such values even when
// nothing is substituted, and so does drydock. Everything else, including
// ConfigMap binaryData, is processed as plain text.
func replaceString(value string, opts Options) (string, bool) {
	if opts.Kind == "Secret" {
		if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && genericPlaceholder.Match(decoded) {
			inner, _ := replacePlainString(string(decoded), opts)
			out := base64.StdEncoding.EncodeToString([]byte(inner))
			return out, out != value
		}
	}
	return replacePlainString(value, opts)
}

// replacePlainString mirrors genericReplacement (util.go:105-203): the regex
// is selected on annotation key presence and every match, leftmost-first and
// non-overlapping, is replaced whole.
func replacePlainString(value string, opts Options) (string, bool) {
	if !strings.Contains(value, "<") {
		return value, false
	}
	placeholderRegex := specificPathPlaceholder
	if opts.PathAnnotationPresent {
		placeholderRegex = genericPlaceholder
	}

	changed := false
	out := placeholderRegex.ReplaceAllStringFunc(value, func(match string) string {
		identity, ok := placeholderIdentity(match, opts)
		if !ok {
			return match
		}
		changed = true
		return redactedValue(identity)
	})
	if !changed {
		return value, false
	}
	return out, true
}

// placeholderIdentity derives the marker identity of one regex match the way
// AVP derives its lookup (util.go:118-149): strip every leading and trailing
// '<' and '>', split off '|' modifiers, trim spaces (only spaces) from the
// first field, and test the unanchored inline syntax. An inline token
// resolves to its own path, key and optional version regardless of the
// annotation; anything else is a key in the annotation path's data, which
// only exists when the annotation is present and non-empty. Modifiers do not
// change what AVP looks up, so they are not part of the identity.
func placeholderIdentity(match string, opts Options) (string, bool) {
	placeholder := strings.Trim(match, "<>")
	pipelineFields := strings.Split(placeholder, "|")
	placeholder = strings.Trim(pipelineFields[0], " ")

	if sub := indivPlaceholderSyntax.FindStringSubmatch(placeholder); sub != nil {
		identity := "path:" + sub[indivPathIndex] + "#" + strings.TrimSpace(sub[indivKeyIndex])
		if version := sub[indivVersionIndex]; version != "" {
			identity += "#" + version
		}
		return identity, true
	}
	if !opts.PathAnnotationPresent || opts.Path == "" {
		return "", false
	}
	return "path:" + opts.Path + "#" + placeholder, true
}

// replaceValue mirrors replaceInner's walk (util.go:44-101). Maps recurse,
// lists are walked only one level deep (see replaceAnySlice) and scalars other
// than strings are untouched. unstructured objects only ever contain
// map[string]any and []any; the other map and slice types are accepted for
// the Helm values pre-template pass, which has no AVP analogue, and follow
// the same rules.
func replaceValue(value any, opts Options) (any, bool) {
	switch typed := value.(type) {
	case string:
		return replaceString(typed, opts)
	case []any:
		return replaceAnySlice(typed, opts)
	case []string:
		return replaceStringSlice(typed, opts)
	case map[string]any:
		return replaceStringAnyMap(typed, opts)
	case map[string]string:
		return replaceStringStringMap(typed, opts)
	case map[any]any:
		return replaceAnyMap(typed, opts)
	default:
		return value, false
	}
}

// replaceAnySlice mirrors replaceInner's slice branch (util.go:54-72): only
// string elements are replaced and only map[string]interface{} elements are
// recursed into. Every other element type, in particular a nested list and
// everything inside it, is left untouched.
func replaceAnySlice(values []any, opts Options) (any, bool) {
	replaced := make([]any, len(values))
	changed := false
	for i, item := range values {
		var next any
		itemChanged := false
		switch typed := item.(type) {
		case string:
			next, itemChanged = replaceString(typed, opts)
		case map[string]any:
			next, itemChanged = replaceStringAnyMap(typed, opts)
		default:
			next = item
		}
		replaced[i] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceStringSlice(values []string, opts Options) (any, bool) {
	replaced := make([]string, len(values))
	changed := false
	for i, item := range values {
		next, itemChanged := replaceString(item, opts)
		replaced[i] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceStringAnyMap(values map[string]any, opts Options) (any, bool) {
	replaced := make(map[string]any, len(values))
	changed := false
	for key, item := range values {
		next, itemChanged := replaceValue(item, opts)
		replaced[key] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceStringStringMap(values map[string]string, opts Options) (any, bool) {
	replaced := make(map[string]string, len(values))
	changed := false
	for key, item := range values {
		next, itemChanged := replaceString(item, opts)
		replaced[key] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceAnyMap(values map[any]any, opts Options) (any, bool) {
	replaced := make(map[any]any, len(values))
	changed := false
	for key, item := range values {
		next, itemChanged := replaceValue(item, opts)
		replaced[key] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func redactedValue(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return redactedPrefix + hex.EncodeToString(sum[:])[:12]
}
