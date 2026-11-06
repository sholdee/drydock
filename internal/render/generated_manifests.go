package render

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/sholdee/drydock/internal/manifestyaml"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// generatedJSONGuessSize is how far into a stream gitops-engine
// kube.SplitYAMLToString lets NewYAMLOrJSONDecoder look to tell JSON from
// YAML.
const generatedJSONGuessSize = 4096

// generatedObject is one object that a source hands to the manifest loop of
// the repo-server's GenerateManifests: an object that findManifests reads
// from a directory file, or an object that kube.SplitYAML reads from Helm,
// Kustomize, or config management plugin output.
type generatedObject struct {
	path string
	// label names the object in errors, such as "apps/a.yaml document 1".
	label string
	// object is nil for a JSON null in the array that a Jsonnet file
	// returns. GenerateManifests skips it.
	object *unstructured.Unstructured
	// jsonKindless marks a ".json" object without a kind that the
	// repo-server kept because the content is also an OCI image manifest.
	jsonKindless bool
}

// generatedTarget is one manifest that GenerateManifests emits.
type generatedTarget struct {
	manifest     Manifest
	label        string
	jsonKindless bool
}

// generateManifests is the manifest loop of GenerateManifests
// (reposerver/repository/repository.go:1817-1852 at v3.6.1). An object whose
// items field is an array is a list (Unstructured.IsList): it emits each item,
// and each item must be an object (Unstructured.EachListItem). An object whose
// items field is null and that has neither spec nor status emits nothing
// (isNullList). Every other object is emitted unchanged.
//
// The repo-server then adds the tracking metadata to each emitted manifest,
// and fails the source when it cannot. track does the same on a copy of each
// manifest, so that the source fails here, in the same order. The
// Application layer adds the tracking metadata it keeps later. A nil track
// skips the check.
func generateManifests(objects []generatedObject, track func(*unstructured.Unstructured) error) ([]generatedTarget, error) {
	var out []generatedTarget
	for _, obj := range objects {
		if obj.object == nil {
			continue
		}
		var targets []generatedTarget
		switch {
		case obj.object.IsList():
			items, _ := obj.object.Object["items"].([]any)
			for index, item := range items {
				label := fmt.Sprintf("%s list item /items/%d", obj.label, index)
				itemObject, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s is not an object", label)
				}
				targets = append(targets, generatedTarget{
					manifest: Manifest{Path: obj.path, Object: &unstructured.Unstructured{Object: itemObject}, RootObject: obj.object},
					label:    label,
				})
			}
		case isNullList(obj.object):
			continue
		default:
			targets = []generatedTarget{{
				manifest:     Manifest{Path: obj.path, Object: obj.object},
				label:        obj.label,
				jsonKindless: obj.jsonKindless,
			}}
		}
		for _, target := range targets {
			if track != nil {
				if err := track(target.manifest.Object.DeepCopy()); err != nil {
					return nil, fmt.Errorf("%s: %w", target.label, err)
				}
			}
			out = append(out, target)
		}
	}
	return out, nil
}

// checkManifestKinds fails on the first emitted manifest without a kind, as
// the controller does when it reads the manifests back (unmarshalManifests,
// controller/state.go:562-571 at v3.6.1, through
// v1alpha1.UnmarshalToUnstructured). See unstructuredDecodeKind for what
// counts as a kind.
func checkManifestKinds(targets []generatedTarget) error {
	for _, target := range targets {
		if unstructuredDecodeKind(target.manifest.Object) != "" {
			continue
		}
		if target.jsonKindless {
			return directoryKindlessJSONError(target.manifest.Path, target.manifest.Object)
		}
		return kindlessManifestError(target.label, target.manifest.Object)
	}
	return nil
}

// targetManifests returns the manifests of targets, in the order in which
// Argo CD reads them. The controller keeps the last copy of a repeated
// resource in that order (NormalizeTargetObjects, controller/state.go:
// 575-623 at v3.6.1).
func targetManifests(targets []generatedTarget) []Manifest {
	out := make([]Manifest, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.manifest)
	}
	return out
}

// unstructuredDecodeKind is the kind that the Unstructured JSON decode
// checks (apimachinery v0.37.0 unstructured/helpers.go:388-391). The decode
// reads the parsed GroupVersionKind, not the raw kind field, and
// GroupVersionKind is empty when the apiVersion does not parse as a group
// version, such as an apiVersion with two or more slashes
// (unstructured.go:430-437, runtime/schema/group_version.go:213-226). A kind
// that is not a string also counts as missing.
func unstructuredDecodeKind(object *unstructured.Unstructured) string {
	return object.GroupVersionKind().Kind
}

// missingKindReason says why unstructuredDecodeKind found no kind.
func missingKindReason(object *unstructured.Unstructured) string {
	if object.GetKind() != "" {
		return fmt.Sprintf("has apiVersion %q, which is not a valid group version, so it has no kind", object.GetAPIVersion())
	}
	return "has no kind"
}

// isNullList mirrors the repo-server's isNullList
// (reposerver/repository/repository.go:1988-2000 at v3.6.1): an object with
// "items: null" and neither spec nor status emits nothing.
func isNullList(object *unstructured.Unstructured) bool {
	if _, ok := object.Object["spec"]; ok {
		return false
	}
	if _, ok := object.Object["status"]; ok {
		return false
	}
	items, ok := object.Object["items"]
	return ok && items == nil
}

// kindlessManifestError is the source failure for an object without a kind
// that is not a ".json" directory file. Argo CD fails such an object either
// in the repo-server, when kube.SplitYAML decodes generated output, or in the
// controller, when it reads back a list item that GenerateManifests emitted
// (unmarshalManifests, controller/state.go:562-571 at v3.6.1). Both report
// the apimachinery missing-kind error, which quotes the object instead of
// naming a file, so drydock names the document instead.
func kindlessManifestError(label string, object *unstructured.Unstructured) error {
	return fmt.Errorf("%s %s, so Argo CD fails the Application with \"Object 'Kind' is missing\"", label, missingKindReason(object))
}

// generatedDocument is one document of generated output that
// kube.SplitYAMLToString keeps: its JSON text, not yet decoded into an
// object.
type generatedDocument struct {
	path  string
	label string
	raw   []byte
}

// rawExtensionDecoder is the Decode method of apimachinery's
// YAMLOrJSONDecoder and YAMLToJSONDecoder.
type rawExtensionDecoder interface {
	Decode(into any) error
}

// splitGeneratedDocuments is gitops-engine kube.SplitYAMLToString
// (pkg/utils/kube/kube.go:367-388 at v3.6.1). It reads each document into a
// RawExtension, trims it, and drops an empty or null document, such as a
// document that holds only comments. It stops at the first document that
// does not decode. The label of a document counts every document the
// decoder returns, empty ones included, from firstIndex. It appends to out.
func splitGeneratedDocuments(out []generatedDocument, path string, decoder rawExtensionDecoder, firstIndex int) ([]generatedDocument, error) {
	for index := firstIndex; ; index++ {
		var ext runtime.RawExtension
		if err := decoder.Decode(&ext); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("%s document %d: failed to unmarshal manifest: %w", path, index, manifestyaml.SanitizeDecodeError(err))
		}
		raw := bytes.TrimSpace(ext.Raw)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		out = append(out, generatedDocument{
			path:  path,
			label: fmt.Sprintf("%s document %d", path, index),
			raw:   raw,
		})
	}
}

// decodeGeneratedDocuments is the second loop of gitops-engine kube.SplitYAML
// (pkg/utils/kube/kube.go:348-362 at v3.6.1). It runs only after
// splitGeneratedDocuments read every document, so a document that does not
// parse wins over an earlier document without a kind. Each document is
// decoded with sigs.k8s.io/yaml into an Unstructured, as upstream does. That
// decode fails on a document that is not an object and on an object without
// a kind (see unstructuredDecodeKind), the empty object {} included.
func decodeGeneratedDocuments(docs []generatedDocument) ([]generatedObject, error) {
	objects := make([]generatedObject, 0, len(docs))
	for _, doc := range docs {
		object := &unstructured.Unstructured{}
		if err := sigsyaml.Unmarshal(doc.raw, object); err != nil {
			if isMissingKindError(err) {
				// The Unstructured decode stores the object before it
				// checks the kind.
				return nil, kindlessManifestError(doc.label, object)
			}
			return nil, fmt.Errorf("%s: failed to unmarshal manifest: %w", doc.label, manifestyaml.SanitizeDecodeError(err))
		}
		objects = append(objects, generatedObject{
			path:   doc.path,
			label:  doc.label,
			object: object,
		})
	}
	return objects, nil
}

// isMissingKindError reports whether err wraps the apimachinery missing-kind
// error. runtime.IsMissingKind does not look through wrapped errors.
func isMissingKindError(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if runtime.IsMissingKind(err) {
			return true
		}
	}
	return false
}

// generatedManifests runs the repo-server and controller stages that follow
// kube.SplitYAML: the GenerateManifests loop (generateManifests) and the
// controller kind check (checkManifestKinds).
func generatedManifests(docs []generatedDocument, track func(*unstructured.Unstructured) error) ([]Manifest, error) {
	objects, err := decodeGeneratedDocuments(docs)
	if err != nil {
		return nil, err
	}
	targets, err := generateManifests(objects, track)
	if err != nil {
		return nil, err
	}
	if err := checkManifestKinds(targets); err != nil {
		return nil, err
	}
	return targetManifests(targets), nil
}

// DecodeGeneratedOutput reads Kustomize or config management plugin output
// as Argo CD v3.6.1 does, and fails at the same stage:
//
//  1. kube.SplitYAML (gitops-engine pkg/utils/kube/kube.go:348-388) splits
//     the stream into documents and stops at the first one that does not
//     parse. Then it decodes each document into an Unstructured, which fails
//     on a document that is not an object or has no kind.
//  2. GenerateManifests (reposerver/repository/repository.go:1816-1852)
//     splits lists, drops null lists, and sets the tracking metadata through
//     track (see generateManifests).
//  3. The controller fails on an emitted manifest without a kind, such as a
//     list item (see checkManifestKinds).
//
// Before these stages, Argo CD removes one line break from the end of the
// command output (util/exec/exec.go:285 for kustomize build, and
// cmpserver/plugin/plugin.go:155 for the generate command of a config
// management plugin), and DecodeGeneratedOutput does the same. Only a "|+"
// block scalar at the end of the last document shows it.
//
// path names the output in errors, with the document index. A nil track
// skips the tracking check.
func DecodeGeneratedOutput(path string, data []byte, track func(*unstructured.Unstructured) error) ([]Manifest, error) {
	data = bytes.TrimSuffix(data, []byte("\n"))
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), generatedJSONGuessSize)
	docs, err := splitGeneratedDocuments(nil, path, decoder, 0)
	if err != nil {
		return nil, err
	}
	return generatedManifests(docs, track)
}
