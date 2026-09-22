package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"slices"

	"github.com/PlakarKorp/kloset/connectors"
	yamlv3 "go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
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
	if ref := controllerOf(meta.OwnerReferences); ref != nil {
		group := schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).Group
		return fmt.Sprintf("owned by %s/%s %s, recreated by its controller",
			group, ref.Kind, ref.Name), nil
	}

	return "", nil
}

func (k *k8s) apply(ctx context.Context, name string, rd io.Reader) error {
	var (
		obj = &unstructured.Unstructured{Object: map[string]any{}}
		dec = yamlv3.NewDecoder(rd)
		err = dec.Decode(&obj.Object)
	)
	if err != nil {
		return err
	}

	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")

	gvk := obj.GroupVersionKind()

	meta, err := objectMeta(obj)
	if err != nil {
		return err
	}

	reason, err := k.skipRestore(gvk, meta)
	if err != nil {
		return err
	}
	if reason != "" {
		log.Printf("skipping %s: %s", name, reason)
		return nil
	}

	rest, err := k.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return err
	}

	gvr := rest.Resource

	verbs, err := resourceVerbs(ctx, k.discovercache, gvr)
	if err != nil {
		return err
	}

	if !isRestorable(verbs) {
		return nil
	}

	client := k.dclient.Resource(gvr)

	var ri dynamic.ResourceInterface = client
	if ns := obj.GetNamespace(); ns != "" {
		ri = client.Namespace(ns)
	}

	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
		FieldManager: "plakar-k8s-exporter",
	})
	return err
}

func (k *k8s) restoreConfig(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		if err := k.apply(ctx, record.Pathname, record.Reader); err != nil {
			results <- record.Error(err)
			return err
		}

		results <- record.Ok()
	}

	return nil
}
