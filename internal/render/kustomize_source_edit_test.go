package render

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/filedigest"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	sigsyaml "sigs.k8s.io/yaml"
)

// The tables in TestEditSetKustomizeImagesMatchesKustomize,
// TestEditSetKustomizeReplicasMatchesKustomize and
// TestEditAddKustomizeLabelsWithoutSelectorMatchesKustomize are TestSetImage
// (commands/edit/set/setimage_test.go:15-436), TestSetReplicas
// (commands/edit/set/setreplicas_test.go:15-109) and
// TestAddLabelWithoutSelector (commands/edit/add/addmetadata_test.go:
// 291-418) of sigs.k8s.io/kustomize/kustomize/v5 v5.8.3, copied without
// change. Importing that module would add it to go.mod.

func decodeEditTestKustomization(t *testing.T, content string) types.Kustomization {
	t.Helper()
	if content == "" {
		content = "resources: []\n"
	}
	kustomization, err := decodeKustomization("kustomization.yaml", []byte(content))
	if err != nil {
		t.Fatalf("decodeKustomization() error = %v", err)
	}
	return kustomization
}

// sameKustomizeEditError reports whether got is the kustomize error want,
// which drydock prefixes with the command.
func sameKustomizeEditError(got, want error) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return strings.HasSuffix(got.Error(), ": "+want.Error())
}

func marshalEditTestField(t *testing.T, field string, value any) string {
	t.Helper()
	data, err := sigsyaml.Marshal(map[string]any{field: value})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(data)
}

func TestEditSetKustomizeImagesMatchesKustomize(t *testing.T) {
	errImageNoArgs := errors.New("no image specified")
	errImageInvalidArgs := errKustomizeEditImageInvalid
	type given struct {
		args         []string
		infileImages []string
	}
	type expected struct {
		fileOutput []string
		err        error
	}
	testCases := []struct {
		description string
		given       given
		expected    expected
	}{
		{
			given: given{
				args: []string{"image1=my-image1:my-tag"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
				}},
		},
		{
			given: given{
				args: []string{"image1=my-image1:1234"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: \"1234\"",
				}},
		},
		{
			given: given{
				args: []string{"image1=my-image1:3.2e2"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: \"3.2e2\"",
				}},
		},
		{
			given: given{
				args: []string{"image1=my-image1@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image1",
					"  newName: my-image1",
				}},
		},
		{
			given: given{
				args: []string{"image1:my-tag"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newTag: my-tag",
				}},
		},
		{
			given: given{
				args: []string{"image1@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image1",
				}},
		},
		{
			given: given{
				args: []string{"my-image1:my-tag@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: my-image1",
					"  newTag: my-tag",
				}},
		},
		{
			given: given{
				args: []string{"image1=my-image1:my-tag@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
				}},
		},
		{
			description: "<image>=<image>",
			given: given{
				args: []string{"ngnix=localhost:5000/my-project/ngnix"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: ngnix",
					"  newName: localhost:5000/my-project/ngnix",
				}},
		},
		{
			given: given{
				args: []string{"ngnix=localhost:5000/my-project/ngnix:dev-01"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: ngnix",
					"  newName: localhost:5000/my-project/ngnix",
					"  newTag: dev-01",
				}},
		},
		{
			description: "override file",
			given: given{
				args: []string{"image1=foo.bar.foo:8800/foo/image1:foo-bar"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
					"- name: image2",
					"  newName: my-image2",
					"  newTag: my-tag2",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: foo.bar.foo:8800/foo/image1",
					"  newTag: foo-bar",
					"- name: image2",
					"  newName: my-image2",
					"  newTag: my-tag2",
				}},
		},
		{
			description: "override file with patch",
			given: given{
				args: []string{"image1=foo.bar.foo:8800/foo/image1:foo-bar"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
					"- name: image2",
					"  newName: my-image2",
					"  newTag: my-tag2",
					"patchesJson6902:",
					"- patch: |-",
					"    - op: remove",
					"      path: /spec/selector",
					"  target:",
					"    kind: Service",
					"    name: foo",
					"    version: v1",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: foo.bar.foo:8800/foo/image1",
					"  newTag: foo-bar",
					"- name: image2",
					"  newName: my-image2",
					"  newTag: my-tag2",
					"patchesJson6902:",
					"- patch: |-",
					"    - op: remove",
					"      path: /spec/selector",
					"  target:",
					"    kind: Service",
					"    name: foo",
					"    version: v1",
				}},
		},
		{
			description: "override new tag and new name with just a new tag",
			given: given{
				args: []string{"image1:v1"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newTag: v1",
				}},
		},
		{
			description: "multiple args with multiple overrides",
			given: given{
				args: []string{
					"image1=foo.bar.foo:8800/foo/image1:foo-bar",
					"image2=my-image2@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"image3:my-tag",
				},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag1",
					"- name: image2",
					"  newName: my-image2",
					"  newTag: my-tag2",
					"- name: image3",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: foo.bar.foo:8800/foo/image1",
					"  newTag: foo-bar",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image2",
					"  newName: my-image2",
					"- name: image3",
					"  newTag: my-tag",
				}},
		},
		{
			description: "error: no args",
			expected: expected{
				err: errImageNoArgs,
			},
		},
		{
			description: "error: invalid args",
			given: given{
				args: []string{"bad", "args"},
			},
			expected: expected{
				err: errImageInvalidArgs,
			},
		},
		{
			description: "override new tag but keep new name",
			given: given{
				args: []string{"image1=*:v1"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newName: foo.bar.foo:8800/foo/image1",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: foo.bar.foo:8800/foo/image1",
					"  newTag: v1",
				}},
		},
		{
			description: "override new name but keep new tag",
			given: given{
				args: []string{"image1=my-image1:*"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newName: foo.bar.foo:8800/foo/image1",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
				}},
		},
		{
			description: "keep new name and new tag (rare case)",
			given: given{
				args: []string{"image1=*:*"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
					"  newTag: my-tag",
				}},
		},
		{
			description: "do not set asterisk as new name for existing image",
			given: given{
				args: []string{"image1=*:v1"},
				infileImages: []string{
					"images:",
					"- name: image1",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newTag: v1",
				}},
		},
		{
			description: "do not set asterisk as new name",
			given: given{
				args: []string{"image1=*:v1"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newTag: v1",
				}},
		},
		{
			description: "do not set asterisk as new tag",
			given: given{
				args: []string{"image1=my-image1:*"},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- name: image1",
					"  newName: my-image1",
				}},
		},
		{
			description: "keep new name and update digest",
			given: given{
				args: []string{"image2=*@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3"},
				infileImages: []string{
					"images:",
					"- name: image2",
					"  newName: my-image2",
					"  digest: sha256:abcdef12345",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image2",
					"  newName: my-image2",
				}},
		},
		{
			description: "keep new name, remove tag, and set digest",
			given: given{
				args: []string{"image2=*@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3"},
				infileImages: []string{
					"images:",
					"- name: image2",
					"  newName: my-image2",
					"  newTag: my-tag",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image2",
					"  newName: my-image2",
				}},
		},
		{
			description: "update new name and keep the digest",
			given: given{
				args: []string{"image2=my-image2@*"},
				infileImages: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image2",
					"  newName: foo.bar.foo:8800/foo/image1",
				},
			},
			expected: expected{
				fileOutput: []string{
					"images:",
					"- digest: sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3",
					"  name: image2",
					"  newName: my-image2",
				}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description+strings.Join(tc.given.args, " "), func(t *testing.T) {
			kustomization := decodeEditTestKustomization(t, strings.Join(tc.given.infileImages, "\n"))
			_, err := editSetKustomizeImages(&kustomization, tc.given.args)
			if !sameKustomizeEditError(err, tc.expected.err) {
				t.Fatalf("editSetKustomizeImages() error = %v, want %v", err, tc.expected.err)
			}
			if err != nil {
				return
			}
			// The upstream file output holds every field of the file.
			content := marshalEditTestField(t, "images", kustomization.Images)
			if len(kustomization.PatchesJson6902) != 0 { //nolint:staticcheck // One upstream case keeps patchesJson6902.
				content += marshalEditTestField(t, "patchesJson6902", kustomization.PatchesJson6902) //nolint:staticcheck // One upstream case keeps patchesJson6902.
			}
			if want := strings.Join(tc.expected.fileOutput, "\n"); !strings.Contains(content, want) {
				t.Fatalf("images:\n%s\nwant:\n%s", content, want)
			}
		})
	}
}

func TestEditSetKustomizeReplicasMatchesKustomize(t *testing.T) {
	errReplicasNoArgs := errors.New("no replicas specified")
	errReplicasInvalidArgs := errors.New("invalid format of replica, use the following format: <name>=<count>")
	type given struct {
		args           []string
		infileReplicas []string
	}
	type expected struct {
		fileOutput []string
		err        error
	}
	testCases := []struct {
		description string
		given       given
		expected    expected
	}{
		{
			given: given{
				args: []string{"app=5"},
			},
			expected: expected{
				fileOutput: []string{
					"replicas:",
					"- count: 5",
					"  name: app",
				}},
		},
		{
			description: "override file",
			given: given{
				args: []string{"app=5"},
				infileReplicas: []string{
					"replicas:",
					"- count: 1",
					"  name: app",
					"- count: 2",
					"  name: other-app",
				},
			},
			expected: expected{
				fileOutput: []string{
					"replicas:",
					"- count: 5",
					"  name: app",
					"- count: 2",
					"  name: other-app",
				}},
		},
		{
			description: "multiple args with multiple overrides",
			given: given{
				args: []string{
					"app=100",
					"other-app=200",
				},
				infileReplicas: []string{
					"replicas:",
					"- count: 1",
					"  name: app",
					"- count: 2",
					"  name: other-app",
				},
			},
			expected: expected{
				fileOutput: []string{
					"replicas:",
					"- count: 100",
					"  name: app",
					"- count: 200",
					"  name: other-app",
				}},
		},
		{
			description: "error: no args",
			expected: expected{
				err: errReplicasNoArgs,
			},
		},
		{
			description: "error: invalid args -- no =",
			given: given{
				args: []string{"bad", "args"},
			},
			expected: expected{
				err: errReplicasInvalidArgs,
			},
		},
		{
			description: "error: invalid args -- non-integer count",
			given: given{
				args: []string{"app=bad"},
			},
			expected: expected{
				err: errReplicasInvalidArgs,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description+strings.Join(tc.given.args, " "), func(t *testing.T) {
			kustomization := decodeEditTestKustomization(t, strings.Join(tc.given.infileReplicas, "\n"))
			_, err := editSetKustomizeReplicas(&kustomization, tc.given.args)
			if !sameKustomizeEditError(err, tc.expected.err) {
				t.Fatalf("editSetKustomizeReplicas() error = %v, want %v", err, tc.expected.err)
			}
			if err != nil {
				return
			}
			content := marshalEditTestField(t, "replicas", kustomization.Replicas)
			if want := strings.Join(tc.expected.fileOutput, "\n"); !strings.Contains(content, want) {
				t.Fatalf("replicas:\n%s\nwant:\n%s", content, want)
			}
		})
	}
}

func TestEditAddKustomizeLabelsWithoutSelectorMatchesKustomize(t *testing.T) {
	// The fields of addMetadataOptions in addmetadata.go that the table sets.
	type addMetadataOptions struct {
		force                 bool
		metadata              map[string]string
		labelsWithoutSelector bool
		includeTemplates      bool
	}
	tests := []struct {
		name        string
		baseLabels  []types.Label      // original labels
		options     addMetadataOptions // add labels
		metadata    map[string]string  // added labels
		expected    []types.Label      // expected labels
		expectedErr string
	}{
		{
			name:       "add to empty labels",
			baseLabels: []types.Label{},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				metadata:              map[string]string{"new": "label"},
			},
			expected: []types.Label{
				{
					Pairs: map[string]string{"new": "label"},
				},
			},
		},
		{
			name:       "add to empty labels with IncludeTemplates",
			baseLabels: []types.Label{},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				includeTemplates:      true,
				metadata:              map[string]string{"new": "label"},
			},
			expected: []types.Label{
				{
					Pairs:            map[string]string{"new": "label"},
					IncludeTemplates: true,
				},
			},
		},
		{
			name: "overwrite label requires force",
			baseLabels: []types.Label{
				{
					Pairs: map[string]string{"key1": "old-value1"},
				},
			},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				force:                 false,
				metadata:              map[string]string{"key1": "new-value1"},
			},
			expectedErr: "label key1 already in kustomization file",
		},
		{
			name: "overwrite label",
			baseLabels: []types.Label{
				{
					Pairs: map[string]string{"key1": "old-value1"},
				},
			},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				force:                 true,
				metadata:              map[string]string{"key1": "new-value1"},
			},
			expected: []types.Label{
				{
					Pairs: map[string]string{"key1": "new-value1"},
				},
			},
		},
		{
			name: "overwrite and add label",
			baseLabels: []types.Label{
				{
					Pairs: map[string]string{"key1": "old-value1"},
				},
			},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				force:                 true,
				metadata:              map[string]string{"key1": "new-value1", "key2": "value2"},
			},
			expected: []types.Label{
				{
					Pairs: map[string]string{"key1": "new-value1", "key2": "value2"},
				},
			},
		},
		{
			name:       "add multiple labels without selector",
			baseLabels: []types.Label{},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				metadata:              map[string]string{"test1": "a", "test2": "b"},
			},
			expected: []types.Label{
				{
					Pairs: map[string]string{"test1": "a", "test2": "b"},
				},
			},
		},
		{
			name: "overwrite label with same settings",
			baseLabels: []types.Label{
				{
					Pairs: map[string]string{"key": "old-value"},
				},
				{
					IncludeTemplates: true,
					Pairs:            map[string]string{"key": "old-value"},
				},
			},
			options: addMetadataOptions{
				labelsWithoutSelector: true,
				force:                 true,
				metadata:              map[string]string{"key": "new-value"},
				includeTemplates:      true,
			},
			expected: []types.Label{
				{
					Pairs: map[string]string{"key": "old-value"},
				},
				{
					IncludeTemplates: true,
					Pairs:            map[string]string{"key": "new-value"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := decodeEditTestKustomization(t, "")
			m.Labels = tt.baseLabels
			_, err := editAddKustomizeLabels(&m, tt.options.metadata, kustomizeEditMetadataOptions{
				force:            tt.options.force,
				withoutSelector:  tt.options.labelsWithoutSelector,
				includeTemplates: tt.options.includeTemplates,
			})
			if tt.expectedErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.expectedErr) {
					t.Fatalf("editAddKustomizeLabels() error = %v, want %q", err, tt.expectedErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("editAddKustomizeLabels() error = %v", err)
			}
			if !reflect.DeepEqual(tt.expected, m.Labels) {
				t.Fatalf("labels = %#v, want %#v", m.Labels, tt.expected)
			}
		})
	}
}

// TestParseKustomizeEditArgsMatchesCobra compares parseKustomizeEditArgs
// with cobra v1.10.2 and pflag v1.0.10, the versions kustomize v5.8.3
// builds with, set up as kustomize sets up its edit commands: a root
// command with the persistent --stack-trace flag, and the boolean flags of
// the command.
func TestParseKustomizeEditArgsMatchesCobra(t *testing.T) {
	cases := [][]string{
		{"nginx:1.2"},
		{"-x"},
		{"-"},
		{""},
		{"--"},
		{"--", "-x", "nginx"},
		{"nginx", "--", "--help"},
		{"-h"},
		{"--help"},
		{"nginx", "-h"},
		{"--help=false", "nginx"},
		{"-h=false", "nginx"},
		{"-h=maybe"},
		{"-hx"},
		{"--stack-trace", "nginx"},
		{"--stack-trace"},
		{"--stack-trace=1", "nginx"},
		{"--stack-trace=x"},
		{"--force"},
		{"-f"},
		{"---x"},
		{"--=x"},
		{"--x=y"},
		{"-test.v", "nginx"},
		{"-ftest.v"},
		{"-web=2"},
		{"--web=2"},
		{"-x:v"},
		{"--force=true:v"},
		{"-f:v"},
		{"--without-selector", "a:b"},
		{"--include-templates=false", "a:b"},
		{"-ftest.x:v", "a:b"},
		{"--force", "--force=false", "a:b"},
		{"-f=false", "a:b"},
		{"-f=yes"},
	}
	commands := []struct {
		name  string
		flags []kustomizeEditFlag
	}{
		{name: "set image", flags: kustomizeEditCommonFlags},
		{name: "add label", flags: kustomizeEditLabelFlags},
		{name: "add annotation", flags: kustomizeEditAnnotationFlags},
	}
	for _, command := range commands {
		for _, args := range cases {
			t.Run(command.name+" "+strings.Join(args, " "), func(t *testing.T) {
				wantArgs, wantHelp, wantFlags, wantErr := cobraKustomizeEditArgs(args, command.flags)
				line, err := parseKustomizeEditArgs(command.name, args, command.flags)
				switch {
				case wantErr != nil:
					if err == nil || !strings.HasSuffix(err.Error(), ": "+wantErr.Error()) {
						t.Fatalf("parseKustomizeEditArgs() error = %v, want %v", err, wantErr)
					}
				case err != nil:
					t.Fatalf("parseKustomizeEditArgs() error = %v, want args %q", err, wantArgs)
				case line.help != wantHelp:
					t.Fatalf("help = %t, want %t", line.help, wantHelp)
				case !wantHelp && !slices.Equal(line.args, wantArgs):
					t.Fatalf("args = %q, want %q", line.args, wantArgs)
				}
				if wantErr == nil && !wantHelp {
					for name, want := range wantFlags {
						if line.flags[name] != want {
							t.Fatalf("flag %s = %t, want %t", name, line.flags[name], want)
						}
					}
				}
			})
		}
	}
}

// cobraKustomizeEditArgs runs args through cobra as kustomize does and
// returns the positional arguments, whether cobra printed the help instead
// of running the command, the values of the command's own flags, and the
// error.
func cobraKustomizeEditArgs(args []string, flags []kustomizeEditFlag) ([]string, bool, map[string]bool, error) {
	root := &cobra.Command{Use: "kustomize", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().Bool("stack-trace", false, "print a stack-trace on error")
	var got []string
	ran := false
	command := &cobra.Command{
		Use: "edit",
		RunE: func(_ *cobra.Command, positional []string) error {
			got = positional
			ran = true
			return nil
		},
	}
	for _, flag := range flags {
		if flag.name == "help" || flag.name == "stack-trace" {
			continue
		}
		if flag.shorthand != 0 {
			command.Flags().BoolP(flag.name, string(flag.shorthand), false, "")
		} else {
			command.Flags().Bool(flag.name, false, "")
		}
	}
	root.AddCommand(command)
	root.SetArgs(append([]string{"edit"}, args...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		return nil, false, nil, err
	}
	values := map[string]bool{}
	for _, flag := range flags {
		if flag.name == "help" || flag.name == "stack-trace" {
			continue
		}
		value, err := command.Flags().GetBool(flag.name)
		if err != nil {
			return nil, false, nil, err
		}
		values[flag.name] = value
	}
	return got, !ran, values, nil
}

// TestExpandKustomizeComponentPatternsMatchesKyamlGlob compares the
// component globs with the on-disk Glob of kyaml, which `kustomize edit add
// component` runs from the source directory.
func TestExpandKustomizeComponentPatternsMatchesKyamlGlob(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"app", "components/a", "components/b", "components/.hidden", "components/c/sub", "other/a"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: []\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "kind: ConfigMap\n")
	writeFile(t, filepath.Join(root, "components", "file.yaml"), "x: 1\n")
	patterns := []string{
		"../components/*",
		"../components/.*",
		"../components//*",
		"../components/./a",
		"../components/[ab]",
		"../components/?",
		"../*/a",
		"../comp*/c/*",
		"*",
		"./*",
		"../components/a",
		"../components/a/",
		"../components/[.]hidden",
		"../components/\\a",
	}
	app := filepath.Join(root, "app")
	t.Chdir(app)
	fSys := filesys.MakeFsOnDisk()
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			want, err := fSys.Glob(pattern)
			if err != nil {
				t.Fatalf("Glob() error = %v", err)
			}
			if len(want) == 0 {
				want = []string{pattern}
			}
			got, err := expandKustomizeComponentPatterns(app, root, []string{pattern})
			if err != nil {
				if strings.Contains(err.Error(), "has no match") && len(want) == 1 && want[0] == pattern {
					return
				}
				t.Fatalf("expandKustomizeComponentPatterns() error = %v", err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("matches = %q, want %q", got, want)
			}
		})
	}
	if _, err := expandKustomizeComponentPatterns(app, root, []string{"../components/[a"}); err == nil || !strings.Contains(err.Error(), "error checking the filesystem: syntax error in pattern") {
		t.Fatalf("bad pattern error = %v", err)
	}
}

// The globs never list a directory outside the repository root or in
// .git, and a pattern without matches there fails as kustomize fails a
// missing component.
func TestExpandKustomizeComponentPatternsStaysInRepository(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeFile(t, filepath.Join(parent, "outside", "kustomization.yaml"), "resources: []\n")
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: []\n")
	writeFile(t, filepath.Join(root, ".git", "x", "kustomization.yaml"), "resources: []\n")
	app := filepath.Join(root, "app")
	for _, pattern := range []string{"../../*", "../../outside", "../.git/*"} {
		_, err := expandKustomizeComponentPatterns(app, root, []string{pattern})
		if err == nil || !strings.Contains(err.Error(), pattern+" has no match: must build at directory: not a valid directory") {
			t.Fatalf("expandKustomizeComponentPatterns(%q) error = %v, want no match", pattern, err)
		}
	}
	got, err := expandKustomizeComponentPatterns(app, root, []string{"https://example.invalid/repo//c?ref=v1"})
	if err != nil {
		t.Fatalf("expandKustomizeComponentPatterns() error = %v", err)
	}
	if want := []string{"https://example.invalid/repo//c?ref=v1"}; !slices.Equal(got, want) {
		t.Fatalf("matches = %q, want %q", got, want)
	}
	// kustomize fails these at the edit step with the text of loader.New
	// (kustomize/api v0.21.3, internal/loader/fileloader.go:172-209).
	for pattern, want := range map[string]string{
		"":            " has no match: new root cannot be empty",
		"/nonexist/*": "/nonexist/* has no match: new root '/nonexist/*' cannot be absolute",
	} {
		_, err := expandKustomizeComponentPatterns(app, root, []string{pattern})
		if err == nil || !strings.HasSuffix(err.Error(), "kustomize edit add component: "+want) {
			t.Fatalf("expandKustomizeComponentPatterns(%q) error = %v, want %q", pattern, err, want)
		}
	}
}

// Ported from TestAddComponentHappyPath, TestAddComponentAlreadyThere,
// TestAddKustomizationFileAsComponent, TestAddComponentNoArgs and
// TestAddComponentFileNotFound (commands/edit/add/addcomponent_test.go).
func TestEditAddKustomizeComponentsMatchesKustomize(t *testing.T) {
	const componentFileName = "myWonderfulComponent.yaml"
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "kustomization.yaml"), "resources: []\n")
	writeFile(t, filepath.Join(root, componentFileName), "x: 1\n")
	writeFile(t, filepath.Join(root, componentFileName+"another"), "x: 1\n")

	kustomization := decodeEditTestKustomization(t, "")
	if err := editAddKustomizeComponents(&kustomization, "kustomization.yaml", root, root, []string{componentFileName + "*"}); err != nil {
		t.Fatalf("happy path error = %v", err)
	}
	if want := []string{componentFileName, componentFileName + "another"}; !slices.Equal(kustomization.Components, want) {
		t.Fatalf("components = %q, want %q", kustomization.Components, want)
	}
	if err := editAddKustomizeComponents(&kustomization, "kustomization.yaml", root, root, []string{componentFileName}); err != nil {
		t.Fatalf("already there error = %v", err)
	}
	if err := editAddKustomizeComponents(&kustomization, "kustomization.yaml", root, root, []string{"kustomization.yaml"}); err != nil {
		t.Fatalf("kustomization file error = %v", err)
	}
	if want := []string{componentFileName, componentFileName + "another"}; !slices.Equal(kustomization.Components, want) {
		t.Fatalf("components = %q, want %q", kustomization.Components, want)
	}
	if err := editAddKustomizeComponents(&kustomization, "kustomization.yaml", root, root, nil); err == nil || !strings.HasSuffix(err.Error(), ": must specify a component file") {
		t.Fatalf("no args error = %v", err)
	}
	empty := t.TempDir()
	err := editAddKustomizeComponents(&kustomization, "kustomization.yaml", empty, empty, []string{componentFileName})
	if err == nil || !strings.HasSuffix(err.Error(), ": "+componentFileName+" has no match: must build at directory: not a valid directory") {
		t.Fatalf("file not found error = %v", err)
	}
}

// Ported from the label and annotation tests of
// commands/edit/add/addmetadata_test.go.
func TestEditAddKustomizeMetadataMatchesKustomize(t *testing.T) {
	kustomization := decodeEditTestKustomization(t, "")
	if _, err := editAddKustomizeAnnotations(&kustomization, map[string]string{"owls": "cute", "otters": "adorable"}, false); err != nil {
		t.Fatalf("TestRunAddAnnotation first error = %v", err)
	}
	if _, err := editAddKustomizeAnnotations(&kustomization, map[string]string{"owls": "cute", "otters": "adorable"}, false); err == nil {
		t.Fatal("TestRunAddAnnotation repeat error = nil, want already in kustomization file")
	}
	if _, err := editAddKustomizeAnnotations(&kustomization, map[string]string{"new": "annotation"}, false); err != nil {
		t.Fatalf("TestRunAddAnnotation new error = %v", err)
	}
}

// addKustomizeEditMetadata runs editAddKustomizeLabels or
// editAddKustomizeAnnotations on kustomization and returns the map it
// writes.
func addKustomizeEditMetadata(kustomization *types.Kustomization, kind string, values map[string]string, force bool) (map[string]string, error) {
	if kind == "label" {
		_, err := editAddKustomizeLabels(kustomization, values, kustomizeEditMetadataOptions{force: force})
		return kustomization.CommonLabels, err //nolint:staticcheck // Argo CD adds source labels to commonLabels.
	}
	_, err := editAddKustomizeAnnotations(kustomization, values, force)
	return kustomization.CommonAnnotations, err
}

func TestEditAddKustomizeMetadataArgsMatchKustomize(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, wantKey, wantValue, wantErr string
	}{
		{name: "no key", key: "", value: "nokey", wantErr: "invalid %s: ':nokey' (need k:v pair where v may be quoted)"},
		{name: "quoted", key: "k1", value: `"v1"`, wantKey: "k1", wantValue: "v1"},
		{name: "quoted with colon", key: "k1", value: `"v1:v2"`, wantKey: "k1", wantValue: "v1:v2"},
		{name: "comma", key: "test", value: `{"k1":"v1","k2":"v2"}`, wantKey: "test", wantValue: `{"k1":"v1","k2":"v2"}`},
		{name: "too many colons", key: "key", value: "v1:v2", wantKey: "key", wantValue: "v1:v2"},
		{name: "colon in key", key: "a:b", value: "c", wantKey: "a", wantValue: "b:c"},
		{name: "one quote", key: "q", value: `"`, wantKey: "q", wantValue: `"`},
	} {
		for _, kind := range []string{"label", "annotation"} {
			t.Run(kind+" "+tc.name, func(t *testing.T) {
				kustomization := decodeEditTestKustomization(t, "")
				got, err := addKustomizeEditMetadata(&kustomization, kind, map[string]string{tc.key: tc.value}, false)
				if tc.wantErr != "" {
					want := strings.ReplaceAll(tc.wantErr, "%s", kind)
					if err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
						t.Fatalf("error = %v, want %q", err, want)
					}
					return
				}
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				if got[tc.wantKey] != tc.wantValue || len(got) != 1 {
					t.Fatalf("%s map = %#v, want %s=%q", kind, got, tc.wantKey, tc.wantValue)
				}
			})
		}
	}
}

func TestEditAddKustomizeMetadataForceMatchesKustomize(t *testing.T) {
	for _, kind := range []string{"label", "annotation"} {
		kustomization := decodeEditTestKustomization(t, "")
		add := func(value string, force bool) error {
			_, err := addKustomizeEditMetadata(&kustomization, kind, map[string]string{"key": value}, force)
			return err
		}
		if err := add("foo", false); err != nil {
			t.Fatalf("%s force first error = %v", kind, err)
		}
		// The same value fails too: kustomize checks only the key.
		for _, value := range []string{"bar", "foo"} {
			want := kind + " key already in kustomization file. Use --force to override."
			if err := add(value, false); err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
				t.Fatalf("%s force %s error = %v, want %q", kind, value, err, want)
			}
		}
		if err := add("bar", true); err != nil {
			t.Fatalf("%s force error = %v", kind, err)
		}
	}
	kustomization := decodeEditTestKustomization(t, "")
	_, err := editAddKustomizeLabels(&kustomization, map[string]string{"new": "label"}, kustomizeEditMetadataOptions{includeTemplates: true})
	if err == nil || !strings.Contains(err.Error(), "--without-selector flag must be specified for --include-templates to work") {
		t.Fatalf("include templates error = %v", err)
	}
}

const sourceEditTestDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    app: web
spec:
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: n
          image: nginx:1.0
        - name: a
          image: a
`

// writeSourceEditTestRepo writes apps/demo with kustomization (after
// "resources: [deploy.yaml]") and components/a, components/b and
// components/.hidden, each a component with one ConfigMap.
func writeSourceEditTestRepo(t *testing.T, kustomization string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources:\n  - deploy.yaml\n"+kustomization)
	writeFile(t, filepath.Join(root, "apps", "demo", "deploy.yaml"), sourceEditTestDeployment)
	for _, name := range []string{"a", "b", ".hidden"} {
		writeFile(t, filepath.Join(root, "components", name, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
		writeFile(t, filepath.Join(root, "components", name, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: component"+name+"\n")
	}
	return root
}

func renderSourceEditTest(t *testing.T, root string, kustomize argoappv1.ApplicationSourceKustomize) ([]Manifest, error) {
	t.Helper()
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: filepath.Join("apps", "demo")}, RenderOptions{
		Kustomize: &kustomize,
		ArgoEnv:   argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}},
	})
	return manifests, err
}

func sourceEditTestImages(t *testing.T, manifests []Manifest) []string {
	t.Helper()
	deployment := findManifest(manifests, "Deployment", "web")
	if deployment == nil {
		t.Fatalf("manifests = %#v, missing Deployment", manifests)
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	images := make([]string, 0, len(containers))
	for _, container := range containers {
		fields, _ := container.(map[string]any)
		image, _ := fields["image"].(string)
		images = append(images, image)
	}
	return images
}

// `kustomize edit set image` sorts the images by name, so an image that
// renames to a name the kustomization renames again is renamed twice.
func TestKustomizeSourceImagesFollowEditSetImage(t *testing.T) {
	for _, tc := range []struct {
		name, kustomization string
		images              []string
		want                []string
		wantErr             string
	}{
		{name: "chained rename", kustomization: "images:\n  - name: b\n    newName: c\n", images: []string{"a=b"}, want: []string{"nginx:1.0", "c"}},
		{name: "tag and digest without =", images: []string{"nginx:1.2@sha256:abc"}, want: []string{"nginx:1.2@sha256:abc", "a"}},
		{name: "keep the tag", kustomization: "images:\n  - name: nginx\n    newTag: \"9.9\"\n", images: []string{"nginx=registry.example.invalid/nginx:*"}, want: []string{"registry.example.invalid/nginx:9.9", "a"}},
		{name: "empty new name", images: []string{"nginx="}, want: []string{"nginx:1.0", "a"}},
		{name: "empty new name replaces the entry", kustomization: "images:\n  - name: nginx\n    newTag: \"9.9\"\n", images: []string{"nginx="}, want: []string{"nginx:1.0", "a"}},
		{name: "empty name before =", kustomization: "images:\n  - name: nginx\n    newName: registry.example.invalid/other\n", images: []string{"=nginx:1.2"}, want: []string{"nginx:1.2", "a"}},
		{name: "no space trimmed", images: []string{" nginx:3"}, want: []string{"nginx:1.0", "a"}},
		{name: "last wins", images: []string{"nginx:1", "nginx:2"}, want: []string{"nginx:2", "a"}},
		{name: "build environment", images: []string{"nginx:$ARGOCD_APP_NAME"}, want: []string{"nginx:demo", "a"}},
		{name: "help flag", images: []string{"nginx:5", "--help"}, want: []string{"nginx:1.0", "a"}},
		{name: "bare name", images: []string{"nginx"}, wantErr: "kustomize edit set image: invalid format of image"},
		{name: "flag", images: []string{"-x"}, wantErr: "kustomize edit set image: unknown shorthand flag: 'x' in -x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			images := make(argoappv1.KustomizeImages, 0, len(tc.images))
			for _, image := range tc.images {
				images = append(images, argoappv1.KustomizeImage(image))
			}
			manifests, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, tc.kustomization), argoappv1.ApplicationSourceKustomize{Images: images})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if got := sourceEditTestImages(t, manifests); !slices.Equal(got, tc.want) {
				t.Fatalf("images = %q, want %q", got, tc.want)
			}
		})
	}
}

// commonLabels and commonAnnotations follow `kustomize edit add label` and
// `kustomize edit add annotation`.
func TestKustomizeSourceMetadataFollowsEditAdd(t *testing.T) {
	for _, tc := range []struct {
		name, kustomization string
		kustomize           argoappv1.ApplicationSourceKustomize
		wantLabels          map[string]string
		wantAnnotations     map[string]string
		wantErr             string
	}{
		{
			name:          "same value fails",
			kustomization: "commonLabels:\n  team: a\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "a"}},
			wantErr:       "kustomize edit add label: label team already in kustomization file. Use --force to override.",
		},
		{
			name:          "labels entry not checked",
			kustomization: "labels:\n  - pairs:\n      team: a\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "x"}},
			wantLabels:    map[string]string{"app": "web", "team": "x"},
		},
		{
			// The new labels entry does not fail on the commonLabels key.
			// kustomize applies the labels entries before commonLabels, so
			// commonLabels wins.
			name:          "without selector ignores commonLabels",
			kustomization: "commonLabels:\n  team: a\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "x"}, LabelWithoutSelector: true},
			wantLabels:    map[string]string{"app": "web", "team": "a"},
		},
		{
			name:          "without selector ignores a selector labels entry",
			kustomization: "labels:\n  - pairs:\n      team: a\n    includeSelectors: true\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "x"}, LabelWithoutSelector: true},
			wantLabels:    map[string]string{"app": "web", "team": "x"},
		},
		{
			name:          "without selector checks the matching labels entry",
			kustomization: "labels:\n  - pairs:\n      team: a\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "x"}, LabelWithoutSelector: true},
			wantErr:       "label team already in kustomization file",
		},
		{
			name:      "include templates needs without selector",
			kustomize: argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"zone": "x"}, LabelIncludeTemplates: true},
			wantErr:   "kustomize edit add label: --without-selector flag must be specified for --include-templates to work",
		},
		{
			name:            "quotes trimmed",
			kustomize:       argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"q": `"v"`}, CommonAnnotations: map[string]string{"q": `"$ARGOCD_APP_NAME"`}, CommonAnnotationsEnvsubst: true},
			wantLabels:      map[string]string{"app": "web", "q": "v"},
			wantAnnotations: map[string]string{"q": "demo"},
		},
		{
			name:            "force",
			kustomization:   "commonLabels:\n  team: a\ncommonAnnotations:\n  owner: a\n",
			kustomize:       argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "x"}, ForceCommonLabels: true, CommonAnnotations: map[string]string{"owner": "x"}, ForceCommonAnnotations: true},
			wantLabels:      map[string]string{"app": "web", "team": "x"},
			wantAnnotations: map[string]string{"owner": "x"},
		},
		{
			name:          "annotation same value fails",
			kustomization: "commonAnnotations:\n  owner: a\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonAnnotations: map[string]string{"owner": "a"}},
			wantErr:       "kustomize edit add annotation: annotation owner already in kustomization file. Use --force to override.",
		},
		{
			// pflag reads -f as --force and drops the rest, which starts
			// with "test.", so the other labels are written with force.
			name:          "label key that sets force",
			kustomization: "commonLabels:\n  team: a\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"-ftest.x": "v", "team": "x"}},
			wantLabels:    map[string]string{"app": "web", "team": "x"},
		},
		{
			name:          "labels entry without pairs",
			kustomization: "labels:\n  - pairs: null\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"team": "x"}, LabelWithoutSelector: true},
			wantErr:       "kustomize edit add label: labels entry 0 has no pairs; kustomize v5.8.3 fails on it with a nil map panic",
		},
		{
			name:      "label key read as a flag",
			kustomize: argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"-x": "v"}},
			wantErr:   "kustomize edit add label: unknown shorthand flag: 'x' in -x:v",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifests, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, tc.kustomization), tc.kustomize)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			deployment := findManifest(manifests, "Deployment", "web")
			if deployment == nil {
				t.Fatalf("manifests = %#v, missing Deployment", manifests)
			}
			if tc.wantLabels != nil && !reflect.DeepEqual(deployment.GetLabels(), tc.wantLabels) {
				t.Fatalf("labels = %#v, want %#v", deployment.GetLabels(), tc.wantLabels)
			}
			if tc.wantAnnotations != nil && !reflect.DeepEqual(deployment.GetAnnotations(), tc.wantAnnotations) {
				t.Fatalf("annotations = %#v, want %#v", deployment.GetAnnotations(), tc.wantAnnotations)
			}
		})
	}
}

// components follow `kustomize edit add component`: globs expand without
// hidden entries, and a component already listed is not added again.
func TestKustomizeSourceComponentsFollowEditAddComponent(t *testing.T) {
	for _, tc := range []struct {
		name, kustomization string
		kustomize           argoappv1.ApplicationSourceKustomize
		want                []string
		wantErr             string
	}{
		{name: "repeated", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/a", "../../components/a"}}, want: []string{"componenta"}},
		{name: "already listed", kustomization: "components:\n  - ../../components/a\n", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/a"}}, want: []string{"componenta"}},
		{name: "glob", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/b", "../../components/*"}}, want: []string{"componenta", "componentb"}},
		{name: "hidden glob", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/.*"}}, want: []string{"component.hidden"}},
		{name: "kustomization file", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"kustomization.yaml"}}, want: nil},
		{name: "ignoreMissingComponents kept and listed", kustomization: "components:\n  - ../../components/b\n", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/missing", "../../components/b"}, IgnoreMissingComponents: true}, want: []string{"componentb"}},
		{name: "missing", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/missing"}}, wantErr: "kustomize edit add component: ../../components/missing has no match: must build at directory: not a valid directory"},
		{name: "glob without match", kustomize: argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/z*"}}, wantErr: "../../components/z* has no match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifests, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, tc.kustomization), tc.kustomize)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			var got []string
			for _, manifest := range manifests {
				if manifest.Object.GetKind() == "ConfigMap" {
					got = append(got, manifest.Object.GetName())
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ConfigMaps = %q, want %q", got, tc.want)
			}
		})
	}
}

// Argo CD runs the edit commands in a fixed order, so the first failing
// option decides the error, and it reads the patches field of the
// kustomization file itself when no edit command ran before it.
func TestKustomizeSourceOptionsFailInArgoCDOrder(t *testing.T) {
	patch := argoappv1.KustomizePatch{Patch: "- op: add\n  path: /metadata/labels/p\n  value: x\n", Target: &argoappv1.KustomizeSelector{KustomizeResId: argoappv1.KustomizeResId{KustomizeGvk: argoappv1.KustomizeGvk{Kind: "Deployment"}}}} //nolint:modernize // The embedded fields name the target kind.
	unknownOption := patch
	unknownOption.Options = map[string]bool{"allowNameChange": true, "foo": true}
	for _, tc := range []struct {
		name, kustomization string
		kustomize           argoappv1.ApplicationSourceKustomize
		wantErr             string
	}{
		{
			name:          "images before labels",
			kustomization: "commonLabels:\n  app: web\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{Images: argoappv1.KustomizeImages{"nginx"}, CommonLabels: map[string]string{"app": "x"}},
			wantErr:       "kustomize edit set image: invalid format of image",
		},
		{
			name:      "replicas before labels",
			kustomize: argoappv1.ApplicationSourceKustomize{Replicas: argoappv1.KustomizeReplicas{{Name: "w=b", Count: intstr.FromInt(2)}}, CommonLabels: map[string]string{"-x": "y"}},
			wantErr:   "kustomize edit set replicas: invalid format of replica",
		},
		{
			name:          "labels before annotations",
			kustomization: "commonLabels:\n  app: web\ncommonAnnotations:\n  o: v\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{CommonLabels: map[string]string{"app": "x"}, CommonAnnotations: map[string]string{"o": "w"}},
			wantErr:       "label app already in kustomization file",
		},
		{
			name:          "patches field without a list",
			kustomization: "patches:\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{Patches: argoappv1.KustomizePatches{patch}},
			wantErr:       "expected 'patches' field in kustomization.yaml to be []any, but got <nil>",
		},
		{
			name:      "unknown patch option",
			kustomize: argoappv1.ApplicationSourceKustomize{Patches: argoappv1.KustomizePatches{unknownOption}},
			wantErr:   `invalid Kustomization: json: unknown field "foo"`,
		},
		{
			name:          "help flag does not rewrite the file",
			kustomization: "patches:\n",
			kustomize:     argoappv1.ApplicationSourceKustomize{Images: argoappv1.KustomizeImages{"--help"}, Patches: argoappv1.KustomizePatches{patch}},
			wantErr:       "expected 'patches' field in kustomization.yaml to be []any, but got <nil>",
		},
		{
			name:      "empty component before patch option",
			kustomize: argoappv1.ApplicationSourceKustomize{Patches: argoappv1.KustomizePatches{unknownOption}, Components: []string{""}},
			wantErr:   "kustomize edit add component:  has no match: new root cannot be empty",
		},
		{
			name:      "absolute component before patch option",
			kustomize: argoappv1.ApplicationSourceKustomize{Patches: argoappv1.KustomizePatches{unknownOption}, Components: []string{"/nonexist/*"}},
			wantErr:   "/nonexist/* has no match: new root '/nonexist/*' cannot be absolute",
		},
		{
			name:      "component glob before patch option",
			kustomize: argoappv1.ApplicationSourceKustomize{Patches: argoappv1.KustomizePatches{unknownOption}, Components: []string{"../../components/missing"}},
			wantErr:   "../../components/missing has no match",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, tc.kustomization), tc.kustomize)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
	// kustomize matches the patch fields without regard to letter case.
	rename := argoappv1.KustomizePatch{Patch: "- op: replace\n  path: /metadata/name\n  value: renamed\n", Target: patch.Target}
	for _, key := range []string{"AllowNameChange", "allownamechange"} {
		rename.Options = map[string]bool{key: true}
		manifests, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, ""), argoappv1.ApplicationSourceKustomize{Patches: argoappv1.KustomizePatches{rename}})
		if err != nil {
			t.Fatalf("Render() with option %s error = %v", key, err)
		}
		if findManifest(manifests, "Deployment", "renamed") == nil {
			t.Fatalf("manifests with option %s = %#v, want Deployment renamed", key, manifests)
		}
	}
	// An edit command before the patches rewrites the kustomization file,
	// which drops the empty patches field.
	manifests, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, "patches:\n"), argoappv1.ApplicationSourceKustomize{NamePrefix: "x-", Patches: argoappv1.KustomizePatches{patch}})
	if err != nil {
		t.Fatalf("Render() after an edit error = %v", err)
	}
	if deployment := findManifest(manifests, "Deployment", "x-web"); deployment == nil || deployment.GetLabels()["p"] != "x" {
		t.Fatalf("manifests = %#v, want patched x-web", manifests)
	}
}

// sourceEditTestDigest returns the render cache input paths of apps/demo
// under kustomize, and the filesystem digest of those paths.
func sourceEditTestDigest(t *testing.T, root string, kustomize *argoappv1.ApplicationSourceKustomize) ([]string, string) {
	t.Helper()
	paths, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: filepath.Join("apps", "demo")}, RenderOptions{Kustomize: kustomize})
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	digestPaths := make([]string, 0, len(paths))
	inputs := make([]filedigest.PathDigestPath, 0, len(paths))
	for _, path := range paths {
		digestPaths = append(digestPaths, path.Path)
		inputs = append(inputs, filedigest.PathDigestPath{Path: path.Path, Optional: path.Optional})
	}
	slices.Sort(digestPaths)
	result, err := filedigest.PathDigest(context.Background(), filedigest.PathDigestInput{RepoRoot: root, Paths: inputs})
	if err != nil {
		t.Fatalf("PathDigest() error = %v", err)
	}
	return digestPaths, result.Digest
}

// A component glob changes the render cache key when a match is added,
// and not when an entry that does not match is added: the key records the
// path of each input, and each match is a component whose files are
// inputs. The directory that the glob lists is not an input, so a symlink
// elsewhere in it does not turn the cache off.
func TestKustomizeSourceComponentGlobKeysItsMatches(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	kustomize := &argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/[abc]"}}
	paths, before := sourceEditTestDigest(t, root, kustomize)
	for _, want := range []string{"components/a/kustomization.yaml", "components/b/cm.yaml"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("digest paths = %q, want %q", paths, want)
		}
	}
	if slices.Contains(paths, "components") {
		t.Fatalf("digest paths = %q, want no whole components directory", paths)
	}

	// An entry that does not match, and a symlink, change nothing.
	writeFile(t, filepath.Join(root, "components", "d", "kustomization.yaml"), "kind: Component\n")
	if err := os.Symlink("a", filepath.Join(root, "components", "link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, after := sourceEditTestDigest(t, root, kustomize); after != before {
		t.Fatal("digest changed for an entry that does not match")
	}

	// A new match changes the key.
	writeFile(t, filepath.Join(root, "components", "c", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n")
	paths, after := sourceEditTestDigest(t, root, kustomize)
	if after == before || !slices.Contains(paths, "components/c/kustomization.yaml") {
		t.Fatalf("digest paths = %q, digest unchanged = %t, want components/c and a new digest", paths, after == before)
	}

	// A glob whose directory is the repository root keys its matches only.
	if paths, _ := sourceEditTestDigest(t, root, &argoappv1.ApplicationSourceKustomize{Components: []string{"../../comp*/a"}}); slices.Contains(paths, ".") || !slices.Contains(paths, "components/a/cm.yaml") {
		t.Fatalf("digest paths = %q, want components/a without the root", paths)
	}
}

// For changed-only selection, a component glob owns the directory it
// lists, so a change that removes a match there selects the source.
func TestKustomizeSourceComponentGlobOwnsItsDirectory(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	kustomize := &argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/[ab]"}}
	selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), kustomize)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for _, want := range []string{"components", "components/a/cm.yaml", "components/b/kustomization.yaml"} {
		if !slices.Contains(selection, want) {
			t.Fatalf("selection paths = %q, want %q", selection, want)
		}
	}
	// A glob that lists the repository root owns nothing more for
	// changed-only selection, like a Helm value file glob without a
	// static prefix.
	selection, err = KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), &argoappv1.ApplicationSourceKustomize{Components: []string{"../../comp*/a"}})
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	if slices.Contains(selection, ".") || !slices.Contains(selection, "components/a/cm.yaml") {
		t.Fatalf("selection paths = %q, want components/a without the root", selection)
	}
}

// Under ignoreMissingComponents, Argo CD checks the folder named by the
// entry as written, and then `kustomize edit add component` reads the entry
// as a glob. A folder literally named "[a]" is kept, and the glob adds
// components/a. The kept folder's kustomization file decides the render,
// so it is a render cache input and owned by changed-only selection.
func TestKustomizeSourceIgnoreMissingComponentWithGlobCharacters(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	writeFile(t, filepath.Join(root, "components", "[a]", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
	writeFile(t, filepath.Join(root, "components", "[a]", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: componentbracket\n")
	kustomize := argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/[a]"}, IgnoreMissingComponents: true}
	manifests, err := renderSourceEditTest(t, root, kustomize)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	var got []string
	for _, manifest := range manifests {
		if manifest.Object.GetKind() == "ConfigMap" {
			got = append(got, manifest.Object.GetName())
		}
	}
	if want := []string{"componenta"}; !slices.Equal(got, want) {
		t.Fatalf("ConfigMaps = %q, want %q", got, want)
	}
	const keptFile = "components/[a]/kustomization.yaml"
	if paths, _ := sourceEditTestDigest(t, root, &kustomize); !slices.Contains(paths, keptFile) {
		t.Fatalf("digest paths = %q, want %q", paths, keptFile)
	}
	selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), &kustomize)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	if !slices.Contains(selection, keptFile) {
		t.Fatalf("selection paths = %q, want %q", selection, keptFile)
	}
}

// A component reached through an in-repository symlink to .git fails with
// the symlink error before drydock reads or lists anything under .git.
func TestKustomizeSourceComponentThroughSymlinkToGitFailsFirst(t *testing.T) {
	for _, component := range []string{"../../components/gitlink/sub", "../../components/gitlink/*"} {
		t.Run(component, func(t *testing.T) {
			root := writeSourceEditTestRepo(t, "")
			writeFile(t, filepath.Join(root, ".git", "sub", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - secret-cm.yaml\n")
			writeFile(t, filepath.Join(root, ".git", "config"), "[core]\n")
			if err := os.Symlink(filepath.Join("..", ".git"), filepath.Join(root, "components", "gitlink")); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
			kustomize := argoappv1.ApplicationSourceKustomize{Components: []string{component}}
			_, err := renderSourceEditTest(t, root, kustomize)
			if err == nil || !strings.Contains(err.Error(), `includes symlink component "gitlink"`) {
				t.Fatalf("Render() error = %v, want the symlink error", err)
			}
			for _, leaked := range []string{"secret-cm.yaml", "config", "sub/kustomization"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("Render() error = %v names %q from .git", err, leaked)
				}
			}
			_, digestErr := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: filepath.Join("apps", "demo")}, RenderOptions{Kustomize: &kustomize})
			if digestErr == nil || !strings.Contains(digestErr.Error(), `includes symlink component "gitlink"`) {
				t.Fatalf("KustomizeInputDigestPaths() error = %v, want the symlink error", digestErr)
			}
			for _, leaked := range []string{"secret-cm.yaml", "config", "sub/kustomization"} {
				if strings.Contains(digestErr.Error(), leaked) {
					t.Fatalf("KustomizeInputDigestPaths() error = %v names %q from .git", digestErr, leaked)
				}
			}
			selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), &kustomize)
			if err != nil {
				t.Fatalf("KustomizeSelectionPaths() error = %v", err)
			}
			for _, path := range selection {
				if strings.Contains(path, ".git") || strings.Contains(path, "gitlink/") {
					t.Fatalf("selection paths = %q, want nothing under .git", selection)
				}
			}
		})
	}
}

// A wildcard in a middle part of a component path lists each folder that
// it matches. A symlink there that adds no match, to a folder or to a file,
// adds nothing, as filepath.Glob finds nothing there. A symlink that a
// match passes through fails with the symlink error.
func TestKustomizeSourceComponentGlobSymlinkWithoutMatch(t *testing.T) {
	for _, tc := range []struct {
		name, link, target string
		wantErr            string
	}{
		{name: "folder symlink", link: "zlink", target: "b"},
		{name: "file symlink", link: "README.md", target: filepath.Join("..", "docs", "README.md")},
		{name: "folder symlink to a match", link: "zlink", target: "a", wantErr: `includes symlink component "zlink"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeSourceEditTestRepo(t, "")
			writeFile(t, filepath.Join(root, "components", "a", "sub", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
			writeFile(t, filepath.Join(root, "components", "a", "sub", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: asub\n")
			writeFile(t, filepath.Join(root, "docs", "README.md"), "docs\n")
			if err := os.Symlink(tc.target, filepath.Join(root, "components", tc.link)); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
			kustomize := argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/*/sub"}}
			manifests, err := renderSourceEditTest(t, root, kustomize)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if findManifest(manifests, "ConfigMap", "asub") == nil {
				t.Fatalf("manifests = %#v, want ConfigMap asub", manifests)
			}
			paths, _ := sourceEditTestDigest(t, root, &kustomize)
			if !slices.Contains(paths, "components/a/sub/cm.yaml") {
				t.Fatalf("digest paths = %q, want components/a/sub", paths)
			}
		})
	}
}

// A ".." after a symlink goes up from the symlink's target, as the OS
// resolves it. The glob resolves each element in order, so it finds that
// "zlink/.." is in .git and fails before it lists anything there.
func TestKustomizeSourceComponentGlobParentOfSymlinkIntoGit(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	writeFile(t, filepath.Join(root, ".git", "deep", "x"), "x\n")
	writeFile(t, filepath.Join(root, ".git", "secret-entry", "kustomization.yaml"), "kind: Component\n")
	if err := os.Symlink(filepath.Join("..", ".git", "deep"), filepath.Join(root, "components", "zlink")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	kustomize := argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/zlink/../*"}}
	_, renderErr := renderSourceEditTest(t, root, kustomize)
	_, digestErr := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: filepath.Join("apps", "demo")}, RenderOptions{Kustomize: &kustomize})
	for name, err := range map[string]error{"Render": renderErr, "KustomizeInputDigestPaths": digestErr} {
		if err == nil || !strings.Contains(err.Error(), `includes symlink component "zlink"`) {
			t.Fatalf("%s() error = %v, want the symlink error", name, err)
		}
		for _, leaked := range []string{"secret-entry", "deep", "HEAD"} {
			if strings.Contains(err.Error(), leaked) {
				t.Fatalf("%s() error = %v names %q from .git", name, err, leaked)
			}
		}
	}
}

// Under ignoreMissingComponents, a component in .git is checked as Argo CD
// checks it, by its kustomization file, and then fails the render. Changed-
// only selection never owns or walks it.
func TestKustomizeSourceIgnoreMissingComponentInGit(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	writeFile(t, filepath.Join(root, ".git", "sub", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
	kustomize := argoappv1.ApplicationSourceKustomize{Components: []string{"../../.git/sub", "../../components/b"}, IgnoreMissingComponents: true}
	if _, err := renderSourceEditTest(t, root, kustomize); err == nil || !strings.Contains(err.Error(), "enters a .git directory") {
		t.Fatalf("Render() error = %v, want enters a .git directory", err)
	}
	selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), &kustomize)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for _, path := range selection {
		if strings.HasPrefix(path, ".git") {
			t.Fatalf("selection paths = %q, want nothing in .git", selection)
		}
	}
}

// A ".." after a missing element or a file does not go back, as for the
// OS: the glob finds no match there. kustomize then loads the entry as a
// folder, and ConfirmDir cleans it as text first, so an entry without
// glob characters names the folder after the "..".
func TestKustomizeSourceComponentParentOfFileOrMissing(t *testing.T) {
	for _, tc := range []struct {
		component, want, wantErr string
	}{
		{component: "deploy.yaml/../../../components/*", wantErr: "kustomize edit add component: deploy.yaml/../../../components/* has no match: must build at directory: not a valid directory"},
		{component: "../../nonexist/../components/[ab]", wantErr: "kustomize edit add component: ../../nonexist/../components/[ab] has no match: must build at directory: not a valid directory"},
		{component: "deploy.yaml/../../../components/a", want: "componenta"},
	} {
		t.Run(tc.component, func(t *testing.T) {
			manifests, err := renderSourceEditTest(t, writeSourceEditTestRepo(t, ""), argoappv1.ApplicationSourceKustomize{Components: []string{tc.component}})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if findManifest(manifests, "ConfigMap", tc.want) == nil {
				t.Fatalf("manifests = %#v, want ConfigMap %s", manifests, tc.want)
			}
		})
	}
}

// A glob that fails on a symlink still owns its folder for changed-only
// selection: the folder comes from the pattern text.
func TestKustomizeSourceComponentGlobOwnsItsDirectoryAfterSymlinkError(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	writeFile(t, filepath.Join(root, "components", "a", "sub", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n")
	if err := os.Symlink("a", filepath.Join(root, "components", "zlink")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), &argoappv1.ApplicationSourceKustomize{Components: []string{"../../components/*/sub"}})
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	if !slices.Contains(selection, "components") {
		t.Fatalf("selection paths = %q, want components", selection)
	}
}

// A folder that becomes a symlink to .git between the resolve and the
// open is not listed: the glob makes sure that the open folder is still
// the one it resolved.
func TestKustomizeComponentGlobRejectsFolderSwappedForSymlink(t *testing.T) {
	root := writeSourceEditTestRepo(t, "")
	writeFile(t, filepath.Join(root, ".git", "secret-entry"), "x\n")
	repoRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	defer func() { _ = repoRoot.Close() }()
	glob := newKustomizeComponentGlob(repoRoot, root, filepath.Join(root, "apps", "demo"))
	glob.beforeOpen = func(rel string) {
		if rel != "components" {
			return
		}
		if err := os.Rename(filepath.Join(root, "components"), filepath.Join(root, "components-moved")); err != nil {
			t.Fatalf("Rename() error = %v", err)
		}
		if err := os.Symlink(".git", filepath.Join(root, "components")); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
	}
	matches, err := glob.glob(filepath.Join("..", "..", "components", "*"), 0)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("glob() = %q, %v, want the symlink error", matches, err)
	}
	for _, match := range matches {
		if strings.Contains(match, "secret-entry") {
			t.Fatalf("glob() listed .git: %q", matches)
		}
	}
}

// An absolute symlink target is followed element by element from the
// repository root, so a symlink inside it that leads outside is found
// before a ".." after it is applied.
func TestKustomizeComponentGlobAbsoluteSymlinkTargetLeavingRepository(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources: []\n")
	writeFile(t, filepath.Join(root, "components", "a", "kustomization.yaml"), "kind: Component\n")
	writeFile(t, filepath.Join(parent, "outside", "o2", "o3", "x"), "x\n")
	writeFile(t, filepath.Join(parent, "outside", "o2", "secret-outside", "kustomization.yaml"), "kind: Component\n")
	if err := os.Symlink(filepath.Join(parent, "outside", "o2", "o3"), filepath.Join(root, "L")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := os.Symlink(root+string(filepath.Separator)+"L"+string(filepath.Separator)+"..", filepath.Join(root, "components", "abs")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	// The pattern matches nothing at the repository root, where a text
	// clean of the target would lead, so only the outside check fails it
	// with the symlink error.
	matches, err := expandKustomizeComponentPatterns(filepath.Join(root, "apps", "demo"), root, []string{"../../components/abs/zz*"})
	if err == nil || !strings.Contains(err.Error(), `includes symlink component "abs"`) {
		t.Fatalf("expandKustomizeComponentPatterns() = %q, %v, want the symlink error", matches, err)
	}
	if strings.Contains(err.Error(), "secret-outside") {
		t.Fatalf("error %v names an entry outside the repository", err)
	}
}
