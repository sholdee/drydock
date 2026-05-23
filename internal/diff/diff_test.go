package diff

import (
	"reflect"
	"strings"
	"testing"
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
