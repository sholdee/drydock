package diff

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
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

func TestRunIgnoreJSONPointerSuppressesReplicasDiff(t *testing.T) {
	left := []Document{deploymentDocument("1", nil)}
	right := []Document{deploymentDocument("2", nil)}
	left[0].IgnoreJSONPointers = []string{"/spec/replicas"}
	right[0].IgnoreJSONPointers = []string{"/spec/replicas"}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0: %#v", len(results), results)
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
		IgnoreJSONPointers: []string{"/data/a~1b~0c"},
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

func TestDocumentIgnoreJSONPointersOmittedFromStructuredOutput(t *testing.T) {
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
		for _, forbidden := range []string{"IgnoreJSONPointers", "ignoreJSONPointers", "/data/value"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s output includes ignored JSON pointer field or value %q:\n%s", format, forbidden, body)
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

func configMapDocument(value string, pointers []string) Document {
	return Document{
		Parent: testParent(),
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body:               "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: " + value + "\n",
		IgnoreJSONPointers: pointers,
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
		IgnoreJSONPointers: pointers,
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
		Body:               "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  namespace: default\ndata:\n  password: " + password + "\n  token: " + token + "\n",
		IgnoreJSONPointers: pointers,
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
