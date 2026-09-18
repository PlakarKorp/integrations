package k8s

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/PlakarKorp/kloset/connectors"
	yamlv3 "go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

var restoreVerbs = []string{"create", "patch"}

func resourceVerbs(ctx context.Context, discover discovery.ServerResourcesInterfaceWithContext, gvr schema.GroupVersionResource) (metav1.Verbs, error) {
	list, err := discover.ServerResourcesForGroupVersionWithContext(ctx, gvr.GroupVersion().String())
	if err != nil {
		return nil, err
	}

	for _, res := range list.APIResources {
		if res.Name == gvr.Resource {
			return res.Verbs, nil
		}
	}

	return nil, fmt.Errorf("resource %s not advertised by %s",
		gvr.Resource, gvr.GroupVersion())
}

func isRestorable(verbs metav1.Verbs) bool {
	for _, verb := range restoreVerbs {
		if !slices.Contains(verbs, verb) {
			return false
		}
	}
	return true
}

var neverRestore = Filters{
	{Group: "", Kind: "Node"}:                           defaultFilter,
	{Group: "", Kind: "Event"}:                          defaultFilter,
	{Group: "events.k8s.io", Kind: "Event"}:             defaultFilter,
	{Group: "storage.k8s.io", Kind: "CSINode"}:          defaultFilter,
	{Group: "storage.k8s.io", Kind: "VolumeAttachment"}: defaultFilter,
}

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

func (k *k8s) skipRestore(gvk schema.GroupVersionKind, meta metav1.ObjectMeta) (string, error) {
	gk := gvk.GroupKind()

	if filter, ok := k.restoreFilters[gk]; ok {
		restore, err := filter(meta)
		if err != nil {
			return "", fmt.Errorf("filtering %s/%s %s: %w", gvk.Group, gvk.Kind, meta.Name, err)
		}
		if !restore {
			return fmt.Sprintf("filtered out group/kind: %s/%s", gvk.Group, gvk.Kind), nil
		}
	}

	// not restoring owned objects, since UIDs will be invalid and new ones should be re-created from resource owners
	if ref := controllerOf(meta.OwnerReferences); ref != nil && !k.restoreOwned {
		group := schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).Group
		return fmt.Sprintf("owned by %s/%s %s, recreated by its controller",
			group, ref.Kind, ref.Name), nil
	}

	return "", nil
}

func (k *k8s) apply(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	var (
		discover = memory.NewMemCacheClientWithContext(k.discover)
		mapper   = restmapper.NewDeferredDiscoveryRESTMapperWithContext(discover)
	)

	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		var (
			obj = &unstructured.Unstructured{Object: map[string]any{}}
			dec = yamlv3.NewDecoder(record.Reader)
			err = dec.Decode(&obj.Object)
		)
		if err != nil {
			results <- record.Error(err)
			return err
		}

		unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
		unstructured.RemoveNestedField(obj.Object, "metadata", "uid")

		gvk := obj.GroupVersionKind()

		meta, err := objectMeta(obj)
		if err != nil {
			results <- record.Error(err)
			return err
		}

		reason, err := k.skipRestore(gvk, meta)
		if err != nil {
			results <- record.Error(err)
			return err
		}
		if reason != "" {
			log.Printf("skipping %s: %s", record.Pathname, reason)
			results <- record.Ok()
			continue
		}

		rest, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			results <- record.Error(err)
			return err
		}

		gvr := rest.Resource

		verbs, err := resourceVerbs(ctx, discover, gvr)
		if err != nil {
			results <- record.Error(err)
			return err
		}

		if !isRestorable(verbs) {
			results <- record.Ok()
			continue
		}

		client := k.dclient.Resource(gvr)

		var ri dynamic.ResourceInterface = client
		if ns := obj.GetNamespace(); ns != "" {
			ri = client.Namespace(ns)
		}

		_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
			FieldManager: "plakar-k8s-exporter",
		})
		if err != nil {
			results <- record.Error(err)
			return err
		}

		results <- record.Ok()
	}

	return nil
}
