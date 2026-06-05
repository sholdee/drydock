package diffhtml

import (
	"reflect"
	"testing"
)

func TestLexYAMLLineDottedAndSlashedMappingKey(t *testing.T) {
	assertYAMLTokens(t, "app.kubernetes.io/name: api",
		yamlTokenSpec{class: yamlKeyClass, text: "app.kubernetes.io/name"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
	)
}

func TestLexYAMLLineListItemKeyValue(t *testing.T) {
	assertYAMLTokens(t, "  - name: api",
		yamlTokenSpec{class: yamlPunctuationClass, text: "-"},
		yamlTokenSpec{class: yamlKeyClass, text: "name"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
	)
}

func TestLexYAMLLinePlainScalarValueUnstyled(t *testing.T) {
	assertYAMLTokens(t, "image: ghcr.io/acme/api:1.2.3",
		yamlTokenSpec{class: yamlKeyClass, text: "image"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
	)
}

func TestLexYAMLLineListScalarWithColonIsNotAKey(t *testing.T) {
	assertYAMLTokens(t, "  - --metrics-bind-address=0.0.0.0:19001",
		yamlTokenSpec{class: yamlPunctuationClass, text: "-"},
	)
	assertYAMLTokens(t, "  - app:enabled=true",
		yamlTokenSpec{class: yamlPunctuationClass, text: "-"},
	)
	assertYAMLTokens(t, "  - ghcr.io/acme/api:1.2.3",
		yamlTokenSpec{class: yamlPunctuationClass, text: "-"},
	)
	assertYAMLTokens(t, "  - foo:bar=baz",
		yamlTokenSpec{class: yamlPunctuationClass, text: "-"},
	)
}

func TestLexYAMLLineBoolNumberAndNullValues(t *testing.T) {
	assertYAMLTokens(t, "values: [true, 42, null, ~, -1.5e+2]",
		yamlTokenSpec{class: yamlKeyClass, text: "values"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlPunctuationClass, text: "["},
		yamlTokenSpec{class: yamlBoolClass, text: "true"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ","},
		yamlTokenSpec{class: yamlNumberClass, text: "42"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ","},
		yamlTokenSpec{class: yamlNullClass, text: "null"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ","},
		yamlTokenSpec{class: yamlNullClass, text: "~"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ","},
		yamlTokenSpec{class: yamlNumberClass, text: "-1.5e+2"},
		yamlTokenSpec{class: yamlPunctuationClass, text: "]"},
	)
}

func TestLexYAMLLineQuotedStringWithHashAndComment(t *testing.T) {
	assertYAMLTokens(t, `message: "has # inside" # comment`,
		yamlTokenSpec{class: yamlKeyClass, text: "message"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlStringClass, text: `"has # inside"`},
		yamlTokenSpec{class: yamlCommentClass, text: "# comment"},
	)
}

func TestLexYAMLLineCommentsUseRuneOffsets(t *testing.T) {
	assertYAMLTokens(t, "name: café # note",
		yamlTokenSpec{class: yamlKeyClass, text: "name"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlCommentClass, text: "# note"},
	)
}

func TestLexYAMLLineAnchorsAliasesAndTags(t *testing.T) {
	assertYAMLTokens(t, "ref: !Secret &main *other",
		yamlTokenSpec{class: yamlKeyClass, text: "ref"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlTagClass, text: "!Secret"},
		yamlTokenSpec{class: yamlAnchorClass, text: "&main"},
		yamlTokenSpec{class: yamlAliasClass, text: "*other"},
	)
}

func TestLexYAMLLineFlowPunctuationAndSimpleKeys(t *testing.T) {
	assertYAMLTokens(t, "selector: {matchLabels: {app.kubernetes.io/name: api, enabled: true}}",
		yamlTokenSpec{class: yamlKeyClass, text: "selector"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlPunctuationClass, text: "{"},
		yamlTokenSpec{class: yamlKeyClass, text: "matchLabels"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlPunctuationClass, text: "{"},
		yamlTokenSpec{class: yamlKeyClass, text: "app.kubernetes.io/name"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ","},
		yamlTokenSpec{class: yamlKeyClass, text: "enabled"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
		yamlTokenSpec{class: yamlBoolClass, text: "true"},
		yamlTokenSpec{class: yamlPunctuationClass, text: "}"},
		yamlTokenSpec{class: yamlPunctuationClass, text: "}"},
	)
}

func TestLexYAMLLineDocMarkers(t *testing.T) {
	assertYAMLTokens(t, "---",
		yamlTokenSpec{class: yamlDocClass, text: "---"},
	)
	assertYAMLTokens(t, "  ...  ",
		yamlTokenSpec{class: yamlDocClass, text: "..."},
	)
	assertYAMLTokens(t, "--- old")
}

func TestLexYAMLLineInvalidPartialLine(t *testing.T) {
	assertYAMLTokens(t, `name: "unterminated # not a comment`,
		yamlTokenSpec{class: yamlKeyClass, text: "name"},
		yamlTokenSpec{class: yamlPunctuationClass, text: ":"},
	)
}

type yamlTokenSpec struct {
	class string
	text  string
}

func assertYAMLTokens(t *testing.T, line string, specs ...yamlTokenSpec) {
	t.Helper()
	want := yamlExpectedTokens(t, line, specs...)
	got := lexYAMLLine(line)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lexYAMLLine(%q) = %#v, want %#v", line, got, want)
	}
}

func yamlExpectedTokens(t *testing.T, line string, specs ...yamlTokenSpec) []syntaxRange {
	t.Helper()
	if len(specs) == 0 {
		return nil
	}
	runes := []rune(line)
	cursor := 0
	tokens := make([]syntaxRange, 0, len(specs))
	for _, spec := range specs {
		start := runeIndexFrom(runes, []rune(spec.text), cursor)
		if start == -1 {
			t.Fatalf("expected token text %q not found in %q after rune offset %d", spec.text, line, cursor)
		}
		end := start + len([]rune(spec.text))
		tokens = append(tokens, syntaxRange{start: start, end: end, class: spec.class})
		cursor = end
	}
	return tokens
}

func runeIndexFrom(haystack, needle []rune, start int) int {
	if len(needle) == 0 {
		return start
	}
	for index := start; index+len(needle) <= len(haystack); index++ {
		if reflect.DeepEqual(haystack[index:index+len(needle)], needle) {
			return index
		}
	}
	return -1
}
