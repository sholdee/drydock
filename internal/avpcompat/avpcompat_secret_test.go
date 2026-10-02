package avpcompat

import (
	"encoding/base64"
	"reflect"
	"testing"
)

func b64(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// Item 1: Template.Replace selects secretReplacement from the top-level kind
// string alone (template.go:70-77), and replaceInner hands it EVERY string in
// the object (util.go:32-103). secretReplacement (util.go:217-227) tries
// base64.StdEncoding.DecodeString on the value; when that succeeds and the
// decoded bytes match the generic regex, the replacement runs on the decoded
// text and the result is re-encoded with base64.StdEncoding.EncodeToString.
// configReplacement (util.go:205-215) and genericReplacement never decode.
func TestReplaceStringSecretKindDecodesBase64(t *testing.T) {
	cases := []struct {
		name string
		in   string
		opts Options
		want string
	}{
		{name: "inline token decoded and re-encoded", in: "PHBhdGg6YSNiPg==", opts: Options{Kind: "Secret"}, want: b64(markerAB)},
		{name: "multi line generic keys", in: b64("user=<a>\npw=<b>"), opts: Options{Kind: "Secret", PathAnnotationPresent: true, Path: "P"}, want: b64("user=" + markerPA + "\npw=" + markerPB)},
		{name: "canonical generic without annotation unchanged", in: b64("user=<a>"), opts: Options{Kind: "Secret"}, want: b64("user=<a>")},
		{name: "newline inside base64 canonicalized", in: "PGE+\n", opts: Options{Kind: "Secret"}, want: "PGE+"},
		{name: "literal placeholder is invalid base64", in: "<path:a#b>", opts: Options{Kind: "Secret"}, want: markerAB},
		{name: "decoded without closing bracket", in: "PGE=", opts: Options{Kind: "Secret"}, want: "PGE="},
		{name: "url alphabet rejected", in: "PGF-", opts: Options{Kind: "Secret"}, want: "PGF-"},
		{name: "non utf8 key substituted", in: "PP///z4=", opts: Options{Kind: "Secret", PathAnnotationPresent: true, Path: "P"}, want: b64(redactedValue("path:P#\xff\xff\xff"))},
		{name: "configmap never decodes", in: "PHBhdGg6YSNiPg==", opts: Options{Kind: "ConfigMap"}, want: "PHBhdGg6YSNiPg=="},
		{name: "sealedsecret never decodes", in: "PHBhdGg6YSNiPg==", opts: Options{Kind: "SealedSecret"}, want: "PHBhdGg6YSNiPg=="},
		{name: "unknown kind never decodes", in: "PHBhdGg6YSNiPg==", opts: Options{}, want: "PHBhdGg6YSNiPg=="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := ReplaceStringWithOptions(tc.in, tc.opts)
			if got != tc.want {
				t.Fatalf("ReplaceStringWithOptions(%q, %+v) = %q, want %q", tc.in, tc.opts, got, tc.want)
			}
			if wantChanged := tc.want != tc.in; changed != wantChanged {
				t.Fatalf("ReplaceStringWithOptions(%q, %+v) changed = %v, want %v", tc.in, tc.opts, changed, wantChanged)
			}
		})
	}
}

func TestReplaceValueSecretKindDecodesEveryString(t *testing.T) {
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":   "demo",
			"labels": map[string]any{"x": "PHBhdGg6YSNiPg=="},
		},
		"type":       "Opaque",
		"data":       map[string]any{"k": "PHBhdGg6YSNiPg=="},
		"stringData": map[string]any{"k": "<path:a#b>"},
		"items":      []any{"PHBhdGg6YSNiPg=="},
	}
	want := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":   "demo",
			"labels": map[string]any{"x": b64(markerAB)},
		},
		"type":       "Opaque",
		"data":       map[string]any{"k": b64(markerAB)},
		"stringData": map[string]any{"k": markerAB},
		"items":      []any{b64(markerAB)},
	}
	got, changed := ReplaceValueWithOptions(secret, Options{Kind: "Secret"})
	if !changed {
		t.Fatal("ReplaceValueWithOptions() changed = false, want true")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplaceValueWithOptions() = %#v, want %#v", got, want)
	}

	configMap := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"data":       map[string]any{"k": "PHBhdGg6YSNiPg=="},
		"binaryData": map[string]any{"k": "PHBhdGg6YSNiPg=="},
	}
	got, changed = ReplaceValueWithOptions(configMap, Options{Kind: "ConfigMap"})
	if changed {
		t.Fatalf("ReplaceValueWithOptions(ConfigMap) changed = true, want false (%#v)", got)
	}

	sealed := map[string]any{
		"apiVersion": "bitnami.com/v1alpha1",
		"kind":       "SealedSecret",
		"spec":       map[string]any{"encryptedData": map[string]any{"k": "PHBhdGg6YSNiPg=="}},
	}
	got, changed = ReplaceValueWithOptions(sealed, Options{Kind: "SealedSecret"})
	if changed {
		t.Fatalf("ReplaceValueWithOptions(SealedSecret) changed = true, want false (%#v)", got)
	}
}
