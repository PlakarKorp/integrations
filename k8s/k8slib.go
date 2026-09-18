package k8s

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func objectMeta(obj *unstructured.Unstructured) (metav1.ObjectMeta, error) {
	var meta metav1.ObjectMeta

	raw, ok := obj.Object["metadata"].(map[string]any)
	if !ok {
		return meta, fmt.Errorf("object has no metadata")
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &meta); err != nil {
		return meta, fmt.Errorf("decoding metadata: %w", err)
	}

	return meta, nil
}

func controllerOf(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return &ref
		}
	}
	return nil
}
