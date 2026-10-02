package avpcompat

import (
	"reflect"
	"testing"
)

// Markers for the identities the parity cases below resolve to. The hashes
// are pinned so a change to the identity derivation is visible as a diff in
// the marker, not hidden by recomputing both sides the same way.
const (
	markerAB     = "drydock-redacted-c2b697a6e41a" // path:a#b
	markerXY     = "drydock-redacted-ed49687a5044" // path:x#y
	markerABCD   = "drydock-redacted-23d7b828bf44" // path:a#b#c#d (key b, version c#d)
	markerABC    = "drydock-redacted-37cef2714115" // path:a#b#c
	markerAgtBC  = "drydock-redacted-df5e0cacff05" // path:a>b#c
	markerPA     = "drydock-redacted-17270f3ea26f" // path:P#a
	markerPB     = "drydock-redacted-f108734ddaea" // path:P#b
	markerPZ     = "drydock-redacted-121d8f556b37" // path:P#z
	markerPX     = "drydock-redacted-4870c4cc1dfc" // path:P#x
	markerPAB    = "drydock-redacted-850b7894d863" // path:P#a b
	markerPAltB  = "drydock-redacted-fb7f661d99cd" // path:P#a<b
	markerPTabA  = "drydock-redacted-507abebc433f" // path:P#\ta\t
	markerPEmpty = "drydock-redacted-4237091c820f" // path:P#
	markerPPathA = "drydock-redacted-b28055fe3407" // path:P#path:a#
	markerPAHB   = "drydock-redacted-e867cef152f8" // path:P#a#b
	markerPBeep  = "drydock-redacted-ab289708c537" // path:P#beep
	markerPBoop  = "drydock-redacted-8db8dedf0156" // path:P#boop
	markerPFoo   = "drydock-redacted-bec5ac144fe7" // path:P#foo
)

// stringParityCase drives one string through the annotation context AVP
// would see: absent (specific regex), or present with the given path (generic
// regex, util.go:113-115).
type stringParityCase struct {
	name    string
	in      string
	present bool
	path    string
	want    string
}

func runStringParityCases(t *testing.T, cases []stringParityCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var changed bool
			if tc.present {
				got, changed = ReplaceStringWithPath(tc.in, tc.path)
			} else {
				got, changed = ReplaceString(tc.in)
			}
			if got != tc.want {
				t.Fatalf("replace(%q, present=%v, path=%q) = %q, want %q", tc.in, tc.present, tc.path, got, tc.want)
			}
			if wantChanged := tc.want != tc.in; changed != wantChanged {
				t.Fatalf("replace(%q, present=%v, path=%q) changed = %v, want %v", tc.in, tc.present, tc.path, changed, wantChanged)
			}
		})
	}
}

func TestMarkerIdentitiesArePinned(t *testing.T) {
	for identity, want := range map[string]string{
		"path:a#b":     markerAB,
		"path:x#y":     markerXY,
		"path:a#b#c#d": markerABCD,
		"path:P#a":     markerPA,
		"path:P#foo":   markerPFoo,
	} {
		if got := redactedValue(identity); got != want {
			t.Fatalf("redactedValue(%q) = %q, want %q", identity, got, want)
		}
	}
}

// Item 2: AVP is regex driven (util.go:117 ReplaceAllFunc, leftmost-first,
// non-overlapping, lazy under (?U)). Without the annotation only a literal
// "<path:" starts a match; with it the match starts at the first '<' on the
// line, so a stray '<' and everything up to the placeholder is swallowed into
// the replaced span, and the unanchored inline syntax (util.go:130) still
// resolves the token inside it.
func TestReplaceStringStrayAngleBracketSpans(t *testing.T) {
	runStringParityCases(t, []stringParityCase{
		{name: "absent stray lt before placeholder", in: "a < b <path:x#y>", want: "a < b " + markerXY},
		{name: "present stray lt swallowed", in: "a < b <path:x#y>", present: true, path: "P", want: "a " + markerXY},
		{name: "present empty stray lt swallowed", in: "a < b <path:x#y>", present: true, path: "", want: "a " + markerXY},
		{name: "absent double brackets", in: "<<path:a#b>>", want: "<" + markerAB + ">"},
		{name: "present double brackets", in: "<<path:a#b>>", present: true, path: "P", want: markerAB + ">"},
		{name: "present stray gt before placeholder", in: "x>y<z>", present: true, path: "P", want: "x>y" + markerPZ},
		{name: "absent trailing extra gt kept", in: "<path:a#b>>", want: markerAB + ">"},
		{name: "present trailing extra gt kept", in: "<path:a#b>>", present: true, path: "P", want: markerAB + ">"},
		{name: "absent stray lt on previous line", in: "a < b\n<path:x#y>", want: "a < b\n" + markerXY},
		{name: "present stray lt on previous line", in: "a < b\n<path:x#y>", present: true, path: "P", want: "a < b\n" + markerXY},
	})
}

// Item 3: AVP selects the generic regex on annotation KEY presence
// (util.go:113-115) while an empty value fetches no data (template.go:33-48),
// so inline tokens matched through the generic regex are substituted and
// every other match is left in place.
func TestReplaceStringEmptyPathAnnotationPresent(t *testing.T) {
	runStringParityCases(t, []stringParityCase{
		{name: "generic key unresolvable", in: "<foo>", present: true, path: "", want: "<foo>"},
		{name: "inline token", in: "<path:a#b>", present: true, path: "", want: markerAB},
		{name: "four part inline token via generic span", in: "<path:a#b#c#d>", present: true, path: "", want: markerABCD},
		{name: "stray lt swallowed", in: "a < b <path:x#y>", present: true, path: "", want: "a " + markerXY},
		{name: "gt inside path stops generic span", in: "<path:a>b#c>", present: true, path: "", want: "<path:a>b#c>"},
		{name: "inline token after prefix text", in: "<foo path:x#y>", present: true, path: "", want: markerXY},
		{name: "absent generic key regression guard", in: "<foo>", want: "<foo>"},
		{name: "present generic key resolves", in: "<foo>", present: true, path: "P", want: markerPFoo},
	})
}

// Item 5: with the annotation present every `(?mU)<(.*)>` match on a line is
// a placeholder (util.go:26), the key is the body after strings.Trim("<>"),
// the '|' split and a space-only Trim (util.go:118-122), with no charset
// restriction. Without the annotation only `(?mU)<path:([^#]+)#([^#]+)(?:#([^#]+))?>`
// matches (util.go:27), which admits spaces, '>' and newlines inside the
// path and key parts but exactly two or three '#'-separated parts.
func TestReplaceStringGenericPlaceholderCharset(t *testing.T) {
	runStringParityCases(t, []stringParityCase{
		{name: "present space in key", in: "<a b>", present: true, path: "P", want: markerPAB},
		{name: "present lt inside key", in: "<a<b>c>", present: true, path: "P", want: markerPAltB + "c>"},
		{name: "present repeated brackets trimmed", in: "<<a>>", present: true, path: "P", want: markerPA + ">"},
		{name: "present modifier stripped", in: "<a|base64encode>", present: true, path: "P", want: markerPA},
		{name: "present spaced modifier stripped", in: "<a | base64encode>", present: true, path: "P", want: markerPA},
		{name: "present tabs kept in key", in: "<\ta\t>", present: true, path: "P", want: markerPTabA},
		{name: "present empty key", in: "<>", present: true, path: "P", want: markerPEmpty},
		{name: "present newline never matches", in: "<a\nb>", present: true, path: "P", want: "<a\nb>"},
		{name: "present inline token after prefix", in: "<foo path:x#y>", present: true, path: "P", want: markerXY},
		{name: "present path prefix without key is generic", in: "<path:a#>", present: true, path: "P", want: markerPPathA},
		{name: "present hash without path literal is generic", in: "<a#b>", present: true, path: "P", want: markerPAHB},
		{name: "present two placeholders", in: "supported options: <beep>, <boop>", present: true, path: "P", want: "supported options: " + markerPBeep + ", " + markerPBoop},
		{name: "absent two generic tokens", in: "supported options: <beep>, <boop>", want: "supported options: <beep>, <boop>"},
		{name: "absent space in key", in: "<a b>", want: "<a b>"},
		{name: "absent bare key", in: "<a>", want: "<a>"},
		{name: "absent leading space before path", in: "< path:a#b >", want: "< path:a#b >"},
		{name: "absent four part token", in: "<path:a#b#c#d>", want: "<path:a#b#c#d>"},
		{name: "absent three part token", in: "<path:a#b#c>", want: markerABC},
		{name: "absent modifier stripped", in: "<path:a#b|base64encode>", want: markerAB},
		{name: "absent gt inside path", in: "<path:a>b#c>", want: markerAgtBC},
		{name: "absent newline inside path", in: "<path:a\nb#c>", want: redactedValue("path:a\nb#c")},
		{name: "present leading space before path", in: "< path:a#b >", present: true, path: "P", want: markerAB},
	})
}

// valueParityCase drives a decoded document through the walk under the same
// annotation contexts as stringParityCase.
type valueParityCase struct {
	name    string
	in      any
	present bool
	path    string
	want    any
	changed bool
}

// Item 4: replaceInner's slice branch (util.go:54-72) only replaces string
// elements and recurses into map[string]interface{} elements; a list element
// that is itself a list is skipped along with everything inside it, at any
// depth. A list that is a map value is still walked.
func TestReplaceValueDoesNotDescendIntoNestedLists(t *testing.T) {
	cases := []valueParityCase{
		{
			name: "list in list skipped",
			in:   map[string]any{"a": []any{[]any{"<path:a#b>"}}},
			want: map[string]any{"a": []any{[]any{"<path:a#b>"}}},
		},
		{
			name: "map in list in list skipped",
			in:   map[string]any{"a": []any{[]any{map[string]any{"k": "<path:a#b>"}}}},
			want: map[string]any{"a": []any{[]any{map[string]any{"k": "<path:a#b>"}}}},
		},
		{
			name: "strings maps and map-valued lists walked",
			in: map[string]any{"a": []any{
				"<path:a#b>",
				map[string]any{"k": "<path:a#b>"},
				map[string]any{"k": []any{"<path:a#b>"}},
			}},
			want: map[string]any{"a": []any{
				markerAB,
				map[string]any{"k": markerAB},
				map[string]any{"k": []any{markerAB}},
			}},
			changed: true,
		},
		{
			name:    "mixed list",
			in:      map[string]any{"a": []any{int64(1), "<path:a#b>", []any{"<path:a#b>"}}},
			want:    map[string]any{"a": []any{int64(1), markerAB, []any{"<path:a#b>"}}},
			changed: true,
		},
		{
			name: "list in list under map in list skipped",
			in:   map[string]any{"a": []any{map[string]any{"k": []any{[]any{"<path:a#b>"}}}}},
			want: map[string]any{"a": []any{map[string]any{"k": []any{[]any{"<path:a#b>"}}}}},
		},
		{
			name:    "generic placeholder in nested list skipped",
			in:      map[string]any{"a": []any{[]any{"<x>"}, "<x>"}},
			present: true,
			path:    "P",
			want:    map[string]any{"a": []any{[]any{"<x>"}, markerPX}},
			changed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got any
			var changed bool
			if tc.present {
				got, changed = ReplaceValueWithPath(tc.in, tc.path)
			} else {
				got, changed = ReplaceValue(tc.in)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v (got %#v)", changed, tc.changed, got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replaced = %#v, want %#v", got, tc.want)
			}
		})
	}
}
