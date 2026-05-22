package manifest

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Identity struct {
	Group     string `json:"group" yaml:"group"`
	Kind      string `json:"kind" yaml:"kind"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name      string `json:"name" yaml:"name"`
}

func IdentityOf(obj *unstructured.Unstructured) Identity {
	gvk := obj.GroupVersionKind()
	return Identity{
		Group:     gvk.Group,
		Kind:      gvk.Kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
}

func (i Identity) String() string {
	prefix := i.Kind
	if i.Group != "" {
		prefix = i.Group + "/" + i.Kind
	}
	if i.Namespace == "" {
		return fmt.Sprintf("%s %s", prefix, i.Name)
	}
	return fmt.Sprintf("%s %s/%s", prefix, i.Namespace, i.Name)
}
