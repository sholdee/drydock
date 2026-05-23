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
