package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestRunParentAwareDiff(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: old\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: new\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Change != ChangeModified {
		t.Fatalf("Change = %q, want %q", results[0].Change, ChangeModified)
	}
	for _, want := range []string{
		"Application: argocd/app-a",
		"-  value: old",
		"+  value: new",
	} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

func TestRunUsesZeroLineRangeForAddedAndRemovedResources(t *testing.T) {
	doc := configMapDocument("new", nil)
	tests := []struct {
		name       string
		left       []Document
		right      []Document
		change     Change
		wantHeader string
	}{
		{
			name:       "added",
			right:      []Document{doc},
			change:     ChangeAdded,
			wantHeader: "@@ -0,0 +1,7 @@",
		},
		{
			name:       "removed",
			left:       []Document{doc},
			change:     ChangeRemoved,
			wantHeader: "@@ -1,7 +0,0 @@",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results, err := Run(test.left, test.right, Options{Unified: 3})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("len(results) = %d, want 1", len(results))
			}
			if results[0].Change != test.change {
				t.Fatalf("Change = %q, want %q", results[0].Change, test.change)
			}
			if !strings.Contains(results[0].Diff, test.wantHeader) {
				t.Fatalf("Diff = %q, want header %q", results[0].Diff, test.wantHeader)
			}
			if strings.Contains(results[0].Diff, "\n \n") {
				t.Fatalf("Diff contains phantom blank context line:\n%s", results[0].Diff)
			}
			if strings.Contains(results[0].Diff, "\n-\n") || strings.Contains(results[0].Diff, "\n+\n") {
				t.Fatalf("Diff contains phantom blank add/remove line:\n%s", results[0].Diff)
			}
		})
	}
}

func TestRunIgnoresSourceMetadataInIdentity(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourceName:  "values",
			SourcePath:  "apps/old",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: old\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 1,
			SourceName:  "rendered",
			SourcePath:  "apps/new",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: new\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1: %#v", len(results), results)
	}
	if results[0].Change != ChangeModified {
		t.Fatalf("Change = %q, want %q", results[0].Change, ChangeModified)
	}
	if results[0].Parent != right[0].Parent {
		t.Fatalf("Parent = %#v, want right parent %#v", results[0].Parent, right[0].Parent)
	}
	for _, want := range []string{
		"Source: 1",
		"name=\"rendered\"",
		"apps/new",
		"-  value: old",
		"+  value: new",
	} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

func TestRunRedactsSecretValuesButReportsChangedKeys(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "creds",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  namespace: default\ndata:\n  password: c2VjcmV0LW9sZA==\n  same: dW5jaGFuZ2Vk\nstringData:\n  token: plain-old\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "creds",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  namespace: default\ndata:\n  password: c2VjcmV0LW5ldw==\n  same: dW5jaGFuZ2Vk\nstringData:\n  token: plain-new\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"c2VjcmV0LW9sZA==", "c2VjcmV0LW5ldw==", "dW5jaGFuZ2Vk", "plain-old", "plain-new"} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked secret value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{
		"Secret: default/creds",
		"password: <redacted-before>",
		"password: <redacted-after>",
		"token: <redacted-before>",
		"token: <redacted-after>",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunRedactsSecretBinaryDataValues(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "tls",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: tls\n  namespace: default\nbinaryData:\n  tls.crt: YmluYXJ5LW9sZA==\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "tls",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: tls\n  namespace: default\nbinaryData:\n  tls.crt: YmluYXJ5LW5ldw==\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"YmluYXJ5LW9sZA==", "YmluYXJ5LW5ldw=="} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked binaryData value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{"tls.crt: <redacted-before>", "tls.crt: <redacted-after>"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunSummarizesConfigMapBinaryDataValues(t *testing.T) {
	left := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "YmluYXJ5LW9sZA==")}
	right := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "YmluYXJ5LW5ldw==")}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"YmluYXJ5LW9sZA==", "YmluYXJ5LW5ldw=="} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked ConfigMap binaryData value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{
		"archive.bin:",
		binaryDataSummary("YmluYXJ5LW9sZA=="),
		binaryDataSummary("YmluYXJ5LW5ldw=="),
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunConfigMapBinaryDataEquivalentDecodedBytesDoNotDiff(t *testing.T) {
	left := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "aGVsbG8=")}
	right := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\nbinaryData:\n  archive.bin: |-\n    aGVs\n    bG8=\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want equivalent decoded binaryData to suppress diff: %#v", len(results), results)
	}
}

func TestRunConfigMapBinaryDataAddedAndRemovedKeysRemainVisible(t *testing.T) {
	left := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\nbinaryData:\n  keep.bin: a2VlcA==\n  removed.bin: cmVtb3ZlZA==\n",
	}}
	right := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\nbinaryData:\n  keep.bin: a2VlcA==\n  added.bin: YWRkZWQ=\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, want := range []string{
		"removed.bin:",
		binaryDataSummary("cmVtb3ZlZA=="),
		"added.bin:",
		binaryDataSummary("YWRkZWQ="),
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
	if strings.Contains(diff, "cmVtb3ZlZA==") || strings.Contains(diff, "YWRkZWQ=") {
		t.Fatalf("Diff leaked added/removed binaryData values:\n%s", diff)
	}
}

func TestRunSummarizesAddedConfigMapBinaryDataResource(t *testing.T) {
	right := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "YWRkZWQ=")}

	results, err := Run(nil, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	if strings.Contains(diff, "YWRkZWQ=") {
		t.Fatalf("Diff leaked added ConfigMap binaryData value:\n%s", diff)
	}
	if want := binaryDataSummary("YWRkZWQ="); !strings.Contains(diff, want) {
		t.Fatalf("Diff = %q, want substring %q", diff, want)
	}
}

func TestRunSummarizesRemovedConfigMapBinaryDataResource(t *testing.T) {
	left := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "cmVtb3ZlZA==")}

	results, err := Run(left, nil, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	if strings.Contains(diff, "cmVtb3ZlZA==") {
		t.Fatalf("Diff leaked removed ConfigMap binaryData value:\n%s", diff)
	}
	if want := binaryDataSummary("cmVtb3ZlZA=="); !strings.Contains(diff, want) {
		t.Fatalf("Diff = %q, want substring %q", diff, want)
	}
}

func TestRunSummarizesMalformedConfigMapBinaryData(t *testing.T) {
	left := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "not-base64-left")}
	right := []Document{configMapBinaryDataDocument("cfg", "archive.bin", "not-base64-right")}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"not-base64-left", "not-base64-right"} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked malformed ConfigMap binaryData value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{
		"archive.bin:",
		binaryDataSummary("not-base64-left"),
		binaryDataSummary("not-base64-right"),
		"invalid-base64",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunKeepsConfigMapDataVisibleWhenBinaryDataIsSummarized(t *testing.T) {
	left := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  script.sh: echo old\nbinaryData:\n  archive.bin: YmluYXJ5\n",
	}}
	right := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  script.sh: echo new\nbinaryData:\n  archive.bin: YmluYXJ5\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, want := range []string{"script.sh: echo old", "script.sh: echo new"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want visible ConfigMap.data substring %q", diff, want)
		}
	}
	if strings.Contains(diff, "YmluYXJ5") {
		t.Fatalf("Diff leaked ConfigMap binaryData while data stayed visible:\n%s", diff)
	}
}

func TestRunDoesNotDecodeDataOnlyConfigMapForBinaryDataSummary(t *testing.T) {
	left := []Document{{
		Parent:        testParent(),
		Resource:      Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: old\n",
		Normalization: Normalization{CompareOptions: CompareOptions{IgnoreResourceStatusField: IgnoreResourceStatusNone}},
	}}
	right := []Document{{
		Parent:        testParent(),
		Resource:      Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: [unterminated\n",
		Normalization: Normalization{CompareOptions: CompareOptions{IgnoreResourceStatusField: IgnoreResourceStatusNone}},
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v, want data-only ConfigMap bodies to skip binaryData summary decoding", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want raw data-only ConfigMap diff preserved", len(results))
	}
	if diff := results[0].Diff; !strings.Contains(diff, "[unterminated") {
		t.Fatalf("Diff = %q, want raw data-only ConfigMap body retained", diff)
	}
}

func TestRunRedactsMalformedConfigMapBinaryDataShapes(t *testing.T) {
	left := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\nbinaryData: raw-scalar-left\n",
	}}
	right := []Document{{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\nbinaryData:\n  archive.bin: 12345\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"raw-scalar-left", "12345"} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked malformed ConfigMap binaryData shape value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{"invalid-field", "invalid-value", "archive.bin:"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunRedactsMalformedSecretBearingFields(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "malformed",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: malformed\n  namespace: default\ndata: scalar-old\nstringData:\n  token:\n    nested: plain-old\nbinaryData:\n  bytes: 1\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "malformed",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: malformed\n  namespace: default\ndata: scalar-new\nstringData:\n  token:\n    nested: plain-new\nbinaryData:\n  bytes: 2\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"scalar-old", "scalar-new", "plain-old", "plain-new", "bytes: 1", "bytes: 2"} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked malformed Secret field value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{
		"data: <redacted-malformed-before>",
		"data: <redacted-malformed-after>",
		"stringData: <redacted-malformed-before>",
		"stringData: <redacted-malformed-after>",
		"binaryData: <redacted-malformed-before>",
		"binaryData: <redacted-malformed-after>",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunStripsAttributes(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\n  labels:\n    helm.sh/chart: demo-1.0.0\n  annotations:\n    app.kubernetes.io/version: 1.0.0\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\n  labels:\n    helm.sh/chart: demo-2.0.0\n  annotations:\n    app.kubernetes.io/version: 2.0.0\n",
	}}

	results, err := Run(left, right, Options{Unified: 3, StripAttrs: []string{"helm.sh/chart", "app.kubernetes.io/version"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0: %#v", len(results), results)
	}
}

func TestRunStripsAttributesAndPreservesOtherDiffs(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\n  labels:\n    helm.sh/chart: demo-1.0.0\n    keep: same\ndata:\n  value: old\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\n  labels:\n    helm.sh/chart: demo-2.0.0\n    keep: same\ndata:\n  value: new\n",
	}}

	results, err := Run(left, right, Options{Unified: 3, StripAttrs: []string{"helm.sh/chart"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if strings.Contains(results[0].Diff, "helm.sh/chart") {
		t.Fatalf("Diff includes stripped attribute:\n%s", results[0].Diff)
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

func TestRunStripsAttributesAndRedactsSecretValues(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "creds",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  labels:\n    app.kubernetes.io/version: 1.0.0\ndata:\n  password: c2VjcmV0LW9sZA==\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace:   "argocd",
			Name:        "app-a",
			SourceIndex: 0,
			SourcePath:  "apps/a",
		},
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "creds",
		},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  labels:\n    app.kubernetes.io/version: 2.0.0\ndata:\n  password: c2VjcmV0LW5ldw==\n",
	}}

	results, err := Run(left, right, Options{Unified: 3, StripAttrs: []string{"app.kubernetes.io/version"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"app.kubernetes.io/version", "c2VjcmV0LW9sZA==", "c2VjcmV0LW5ldw=="} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked stripped or secret value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{"password: <redacted-before>", "password: <redacted-after>"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunDefaultIgnoredFieldsSuppressesHelmMetadataNoise(t *testing.T) {
	left := []Document{helmMetadataDeploymentDocument("demo-1.0.0", "1.0.0", "old-config", "old-secret")}
	right := []Document{helmMetadataDeploymentDocument("demo-2.0.0", "2.0.0", "new-config", "new-secret")}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want default ignored fields to suppress metadata-only diff: %#v", len(results), results)
	}
}

func TestRunDefaultIgnoredFieldsSuppressesIgnoredOnlyPodTemplateMetadata(t *testing.T) {
	left := []Document{helmMetadataDeploymentDocument("demo-1.0.0", "1.0.0", "old-config", "old-secret")}
	right := []Document{deploymentDocumentWithoutPodTemplateMetadata()}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want ignored-only pod template metadata suppressed: %#v", len(results), results)
	}
}

func TestRunShowIgnoredFieldsIncludesDefaultIgnoredHelmMetadata(t *testing.T) {
	left := []Document{helmMetadataDeploymentDocument("demo-1.0.0", "1.0.0", "old-config", "old-secret")}
	right := []Document{helmMetadataDeploymentDocument("demo-2.0.0", "2.0.0", "new-config", "new-secret")}

	results, err := Run(left, right, Options{Unified: 3, ShowIgnoredFields: true})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want ignored fields to be visible", len(results))
	}
	diff := results[0].Diff
	for _, want := range []string{
		"-    helm.sh/chart: demo-1.0.0",
		"+    helm.sh/chart: demo-2.0.0",
		"-    chart: demo-1.0.0",
		"+    chart: demo-2.0.0",
		"-    app.kubernetes.io/version: 1.0.0",
		"+    app.kubernetes.io/version: 2.0.0",
		"-        checksum/config: old-config",
		"+        checksum/config: new-config",
		"-        checksum/secret: old-secret",
		"+        checksum/secret: new-secret",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff missing %q:\n%s", want, diff)
		}
	}
}

func TestRunIgnoreJSONPointerSuppressesReplicasDiff(t *testing.T) {
	left := []Document{deploymentDocument("1", nil)}
	right := []Document{deploymentDocument("2", nil)}
	left[0].Normalization.JSONPointers = []string{"/spec/replicas"}
	right[0].Normalization.JSONPointers = []string{"/spec/replicas"}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0: %#v", len(results), results)
	}
}

func TestRunKnownTypeFieldsSuppressesRolloutPodSpecQuantityDiff(t *testing.T) {
	normalization := Normalization{
		KnownTypeFields: []KnownTypeField{{Field: "spec.template.spec", Type: "core/v1/PodSpec"}},
	}
	left := []Document{rolloutDocumentWithCPU("0.1", normalization)}
	right := []Document{rolloutDocumentWithCPU("100m", normalization)}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want knownTypeFields CPU quantity diff ignored: %#v", len(results), results)
	}
}

func TestRunIgnoreJSONPointerMissingPathsAreNoOp(t *testing.T) {
	left := []Document{configMapDocument("old", []string{"/metadata/annotations/missing", "/data/missing"})}
	right := []Document{configMapDocument("new", []string{"/metadata/annotations/missing", "/data/missing"})}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want original diff preserved", len(results))
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

func TestRunIgnoreJSONPointerInvalidPointersReturnClearError(t *testing.T) {
	tests := []struct {
		name      string
		pointer   string
		wantParts []string
	}{
		{
			name:      "missing leading slash",
			pointer:   "spec/replicas",
			wantParts: []string{"JSON pointer", "must be empty or start with /"},
		},
		{
			name:      "invalid array index",
			pointer:   "/spec/template/spec/containers/web/image",
			wantParts: []string{"JSON pointer", "array index"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := []Document{deploymentDocument("1", []string{tt.pointer})}
			right := []Document{deploymentDocument("1", nil)}

			_, err := Run(left, right, Options{Unified: 3})
			if err == nil {
				t.Fatalf("Run() error = nil, want error")
			}
			for _, want := range tt.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Run() error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func TestRunIgnoreJSONPointerUsesUnionForMatchedResources(t *testing.T) {
	left := []Document{deploymentDocument("1", nil)}
	right := []Document{deploymentDocument("2", []string{"/spec/replicas"})}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0: %#v", len(results), results)
	}
}

func TestRunIgnoreJSONPointerEmptyPointerRemovesWholeDocument(t *testing.T) {
	right := []Document{configMapDocument("new", []string{""})}

	results, err := Run(nil, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want whole-document ignore to suppress add diff: %#v", len(results), results)
	}
}

func TestNormalizeDocumentBodyIgnoreJSONPointerDecodesEscapes(t *testing.T) {
	doc := Document{
		Body: `apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
data:
  a/b~c: remove
  keep: same
`,
		Normalization: Normalization{JSONPointers: []string{"/data/a~1b~0c"}},
	}

	body, err := normalizeDocumentBody(doc, Options{})
	if err != nil {
		t.Fatalf("normalizeDocumentBody() error = %v", err)
	}
	if strings.Contains(body, "a/b~c") || strings.Contains(body, "remove") {
		t.Fatalf("normalized body still contains escaped key target:\n%s", body)
	}
	if !strings.Contains(body, "keep: same") {
		t.Fatalf("normalized body = %q, want kept key", body)
	}
}

func TestNormalizeDocumentBodyIgnoreJSONPointerRemovesArrayElement(t *testing.T) {
	doc := deploymentDocument("1", []string{"/spec/template/spec/containers/0/env/0"})

	body, err := normalizeDocumentBody(doc, Options{})
	if err != nil {
		t.Fatalf("normalizeDocumentBody() error = %v", err)
	}
	for _, forbidden := range []string{"first", "null"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("normalized body contains %q:\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, "name: second") {
		t.Fatalf("normalized body = %q, want remaining array element", body)
	}
}

func TestRunIgnoreJSONPointerSecretRedactionHappensAfterRemoval(t *testing.T) {
	left := []Document{secretDocument("old-password", "old-token", []string{"/data/password"})}
	right := []Document{secretDocument("new-password", "new-token", []string{"/data/password"})}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1: %#v", len(results), results)
	}
	diff := results[0].Diff
	for _, forbidden := range []string{"password", "old-password", "new-password", "old-token", "new-token"} {
		if strings.Contains(diff, forbidden) {
			t.Fatalf("Diff leaked ignored or secret value %q:\n%s", forbidden, diff)
		}
	}
	for _, want := range []string{"token: <redacted-before>", "token: <redacted-after>"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestRunHonorsManagedFieldsManagersFromLeftSide(t *testing.T) {
	left := []Document{{
		Parent:        testParent(),
		Resource:      Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Body:          deploymentWithManagedReplicas("kube-controller-manager", 1),
		Normalization: Normalization{ManagedFieldsManagers: []string{"kube-controller-manager"}},
	}}
	right := []Document{{
		Parent:   testParent(),
		Resource: Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Body:     deploymentWithManagedReplicas("", 2),
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want managed replicas ignored: %#v", len(results), results)
	}
}

func TestRunHonorsManagedFieldsManagersFromRightSide(t *testing.T) {
	left := []Document{{
		Parent:   testParent(),
		Resource: Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Body:     deploymentWithManagedReplicas("", 1),
	}}
	right := []Document{{
		Parent:        testParent(),
		Resource:      Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Body:          deploymentWithManagedReplicas("kube-controller-manager", 2),
		Normalization: Normalization{ManagedFieldsManagers: []string{"kube-controller-manager"}},
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want managed replicas ignored: %#v", len(results), results)
	}
}

func TestRunCompareOptionsDefaultIgnoresStatusDiff(t *testing.T) {
	left := []Document{configMapStatusDocument("old", Normalization{})}
	right := []Document{configMapStatusDocument("new", Normalization{})}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want status-only diff ignored by default: %#v", len(results), results)
	}
}

func TestRunCompareOptionsNoneKeepsStatusDiff(t *testing.T) {
	normalization := Normalization{CompareOptions: CompareOptions{IgnoreResourceStatusField: "none"}}
	left := []Document{configMapStatusDocument("old", normalization)}
	right := []Document{configMapStatusDocument("new", normalization)}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want status diff", len(results))
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

func TestRunCompareOptionsCRDIgnoresOnlyCRDStatus(t *testing.T) {
	normalization := Normalization{CompareOptions: CompareOptions{IgnoreResourceStatusField: "crd"}}
	leftCRD := []Document{crdStatusDocument("old", normalization)}
	rightCRD := []Document{crdStatusDocument("new", normalization)}

	crdResults, err := Run(leftCRD, rightCRD, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run(CRD) error = %v", err)
	}
	if len(crdResults) != 0 {
		t.Fatalf("len(CRD results) = %d, want CRD status ignored: %#v", len(crdResults), crdResults)
	}

	leftConfigMap := []Document{configMapStatusDocument("old", normalization)}
	rightConfigMap := []Document{configMapStatusDocument("new", normalization)}
	configMapResults, err := Run(leftConfigMap, rightConfigMap, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run(ConfigMap) error = %v", err)
	}
	if len(configMapResults) != 1 {
		t.Fatalf("len(ConfigMap results) = %d, want non-CRD status diff", len(configMapResults))
	}
}

func TestRunCompareOptionsUnknownStatusBehavesAsAll(t *testing.T) {
	normalization := Normalization{CompareOptions: CompareOptions{IgnoreResourceStatusField: "typo"}}
	left := []Document{configMapStatusDocument("old", normalization)}
	right := []Document{configMapStatusDocument("new", normalization)}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want unknown status mode to ignore all status: %#v", len(results), results)
	}
}

func TestRunCompareOptionsIgnoresAggregatedClusterRoleRules(t *testing.T) {
	normalization := Normalization{CompareOptions: CompareOptions{IgnoreAggregatedRoles: true, IgnoreResourceStatusField: "none"}}
	left := []Document{aggregatedClusterRoleDocument("old", normalization)}
	right := []Document{aggregatedClusterRoleDocument("new", normalization)}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want aggregated role rules ignored: %#v", len(results), results)
	}
}

func TestDocumentNormalizationOmittedFromStructuredOutput(t *testing.T) {
	doc := configMapDocument("same", []string{"/data/value"})

	jsonBody, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	yamlBody, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}

	for format, body := range map[string]string{
		"json": string(jsonBody),
		"yaml": string(yamlBody),
	} {
		for _, forbidden := range []string{"Normalization", "normalization", "/data/value"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s output includes normalization field or value %q:\n%s", format, forbidden, body)
			}
		}
	}
}

func TestExtractWorkloadImages(t *testing.T) {
	docs := []Document{
		{Body: `
apiVersion: v1
kind: Pod
metadata:
  name: pod
spec:
  containers:
    - name: web
      image: " ghcr.io/example/web:v1 "
  initContainers:
    - name: migrate
      image: ghcr.io/example/migrate:v1
  ephemeralContainers:
    - name: debug
      image: ghcr.io/example/debug:v1
`},
		{Body: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
`},
		{Body: `
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
spec:
  template:
    spec:
      containers:
        - name: db
          image: ghcr.io/example/db:v1
`},
		{Body: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: agent
spec:
  template:
    spec:
      containers:
        - name: agent
          image: ghcr.io/example/agent:v1
`},
		{Body: `
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: replica
spec:
  template:
    spec:
      containers:
        - name: replica
          image: ghcr.io/example/replica:v1
`},
		{Body: `
apiVersion: v1
kind: ReplicationController
metadata:
  name: rc
spec:
  template:
    spec:
      containers:
        - name: rc
          image: ghcr.io/example/rc:v1
`},
		{Body: `
apiVersion: batch/v1
kind: Job
metadata:
  name: job
spec:
  template:
    spec:
      containers:
        - name: job
          image: ghcr.io/example/job:v1
`},
		{Body: `
apiVersion: batch/v1
kind: CronJob
metadata:
  name: cron
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: cron
              image: ghcr.io/example/cron:v1
`},
	}

	images := ExtractImages(docs)
	want := []string{
		"ghcr.io/example/agent:v1",
		"ghcr.io/example/cron:v1",
		"ghcr.io/example/db:v1",
		"ghcr.io/example/debug:v1",
		"ghcr.io/example/job:v1",
		"ghcr.io/example/migrate:v1",
		"ghcr.io/example/rc:v1",
		"ghcr.io/example/replica:v1",
		"ghcr.io/example/web:v1",
	}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("ExtractImages() = %#v, want %#v", images, want)
	}
}

func TestExtractImagesIgnoresConfigMapDataImage(t *testing.T) {
	body := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: config
data:
  image: ghcr.io/example/not-a-workload:v1
`

	images := ExtractImages([]Document{{Body: body}})
	if len(images) != 0 {
		t.Fatalf("ExtractImages() = %#v, want no images", images)
	}
}

func TestExtractImagesIncludesExactImageKeysFromCRDs(t *testing.T) {
	docs := []Document{
		{Body: `
apiVersion: renovate-operator.mogenius.com/v1alpha1
kind: RenovateJob
metadata:
  name: renovate
spec:
  image: renovate/renovate:43.195.6@sha256:72d184865d505d5badc5c3b32a48410096e0d9d7e0d875dae28ee97832178f47
`},
		{Body: `
apiVersion: example.test/v1
kind: ExampleApp
metadata:
  name: nested
spec:
  components:
    - name: api
      image: ghcr.io/example/api:v1
    - name: worker
      template:
        image: ghcr.io/example/worker:v1
`},
	}

	images := ExtractImages(docs)
	want := []string{
		"ghcr.io/example/api:v1",
		"ghcr.io/example/worker:v1",
		"renovate/renovate:43.195.6@sha256:72d184865d505d5badc5c3b32a48410096e0d9d7e0d875dae28ee97832178f47",
	}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("ExtractImages() = %#v, want %#v", images, want)
	}
}

func TestExtractImagesIgnoresNonDeployableExactImageKeys(t *testing.T) {
	docs := []Document{
		{Body: `
apiVersion: v1
kind: Secret
metadata:
  name: secret
spec:
  image: ghcr.io/example/secret:v1
`},
		{Body: `
apiVersion: example.test/v1
kind: ExampleApp
metadata:
  name: metadata
  labels:
    image: ghcr.io/example/metadata:v1
status:
  image: ghcr.io/example/status:v1
spec:
  containerImage: ghcr.io/example/container-image:v1
  description: ghcr.io/example/description:v1
`},
		{Body: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: config
data:
  image: ghcr.io/example/configmap-data:v1
binaryData:
  image: Z2hjci5pby9leGFtcGxlL2NvbmZpZ21hcC1iaW5hcnk6djE=
`},
	}

	images := ExtractImages(docs)
	if len(images) != 0 {
		t.Fatalf("ExtractImages() = %#v, want no images", images)
	}
}

func configMapDocument(value string, pointers []string) Document {
	return Document{
		Parent: testParent(),
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body:          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: " + value + "\n",
		Normalization: Normalization{JSONPointers: pointers},
	}
}

func deploymentDocument(replicas string, pointers []string) Document {
	return Document{
		Parent: testParent(),
		Resource: Resource{
			Group:     "apps",
			Kind:      "Deployment",
			Namespace: "default",
			Name:      "web",
		},
		Body: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
spec:
  replicas: ` + replicas + `
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
          env:
            - name: first
              value: one
            - name: second
              value: two
`,
		Normalization: Normalization{JSONPointers: pointers},
	}
}

func helmMetadataDeploymentDocument(chartVersion, appVersion, checksumConfig, checksumSecret string) Document {
	return Document{
		Parent:   testParent(),
		Resource: Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Body: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
  labels:
    helm.sh/chart: ` + chartVersion + `
    chart: ` + chartVersion + `
    app.kubernetes.io/version: ` + appVersion + `
spec:
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        helm.sh/chart: ` + chartVersion + `
        chart: ` + chartVersion + `
        app.kubernetes.io/version: ` + appVersion + `
      annotations:
        checksum/config: ` + checksumConfig + `
        checksum/secret: ` + checksumSecret + `
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
`,
	}
}

func deploymentDocumentWithoutPodTemplateMetadata() Document {
	return Document{
		Parent:   testParent(),
		Resource: Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
		Body: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
spec:
  selector:
    matchLabels:
      app: web
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
`,
	}
}

func rolloutDocumentWithCPU(cpu string, normalization Normalization) Document {
	return Document{
		Parent: testParent(),
		Resource: Resource{
			Group: "argoproj.io",
			Kind:  "Rollout",
			Name:  "demo-rollout",
		},
		Body: `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: demo-rollout
spec:
  template:
    spec:
      containers:
        - name: app
          image: repo/app:v1
          resources:
            requests:
              cpu: ` + cpu + `
`,
		Normalization: normalization,
	}
}

func secretDocument(password, token string, pointers []string) Document {
	return Document{
		Parent: testParent(),
		Resource: Resource{
			Kind:      "Secret",
			Namespace: "default",
			Name:      "creds",
		},
		Body:          "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  namespace: default\ndata:\n  password: " + password + "\n  token: " + token + "\n",
		Normalization: Normalization{JSONPointers: pointers},
	}
}

func deploymentWithManagedReplicas(manager string, replicas int) string {
	managedFields := ""
	if manager != "" {
		managedFields = `  managedFields:
    - apiVersion: apps/v1
      fieldsType: FieldsV1
      fieldsV1:
        f:spec:
          f:replicas: {}
      manager: ` + manager + `
      operation: Update
`
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
%sspec:
  replicas: %d
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
`, managedFields, replicas)
}

func configMapStatusDocument(value string, normalization Normalization) Document {
	return Document{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "cfg"},
		Body: `apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: default
status:
  value: ` + value + `
`,
		Normalization: normalization,
	}
}

func crdStatusDocument(value string, normalization Normalization) Document {
	return Document{
		Parent:   testParent(),
		Resource: Resource{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition", Name: "widgets.example.com"},
		Body: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
status:
  storedVersions:
    - ` + value + `
`,
		Normalization: normalization,
	}
}

func aggregatedClusterRoleDocument(ruleResource string, normalization Normalization) Document {
	return Document{
		Parent:   testParent(),
		Resource: Resource{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "aggregate-view"},
		Body: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: aggregate-view
aggregationRule:
  clusterRoleSelectors:
    - matchLabels:
        rbac.example.com/aggregate-to-view: "true"
rules:
  - apiGroups: [""]
    resources: ["` + ruleResource + `"]
    verbs: ["get"]
`,
		Normalization: normalization,
	}
}

func configMapBinaryDataDocument(name, key, value string) Document {
	return Document{
		Parent:   testParent(),
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: name},
		Body: `apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + name + `
  namespace: default
binaryData:
  ` + key + `: ` + value + `
`,
	}
}

func testParent() Parent {
	return Parent{
		Namespace:   "argocd",
		Name:        "app-a",
		SourceIndex: 0,
		SourcePath:  "apps/a",
	}
}
