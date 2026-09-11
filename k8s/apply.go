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

var neverRestore = map[schema.GroupKind]string{
	{Group: "", Kind: "Node"}:                           "node topology is rebuilt by the target cluster",
	{Group: "", Kind: "Event"}:                          "observability data, not desired state",
	{Group: "events.k8s.io", Kind: "Event"}:             "observability data, not desired state",
	{Group: "storage.k8s.io", Kind: "CSINode"}:          "CSI driver registration is node-local",
	{Group: "storage.k8s.io", Kind: "VolumeAttachment"}: "records which node a volume is mounted on",
}

func skipRestore(gvk schema.GroupVersionKind) string {
	if reason, ok := neverRestore[gvk.GroupKind()]; ok {
		return reason
	}
	return ""
}

func (k *k8s) apply(ctx context.Context, record *connectors.Record) (*unstructured.Unstructured, error) {
	var (
		obj = &unstructured.Unstructured{Object: map[string]any{}}
		dec = yamlv3.NewDecoder(record.Reader)
		err = dec.Decode(&obj.Object)
	)
	if err != nil {
		return nil, err
	}

	if meta, ok := obj.Object["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
		delete(meta, "uid")
	}

	gvk := obj.GroupVersionKind()

	if reason := skipRestore(gvk); reason != "" {
		log.Printf("skipping %s: %s", record.Pathname, reason)
		return obj, nil
	}

	rest, err := k.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	gvr := rest.Resource

	verbs, err := resourceVerbs(ctx, k.discover, gvr)
	if err != nil {
		return nil, err
	}

	if !isRestorable(verbs) {
		return obj, nil
	}

	client := k.dclient.Resource(gvr)

	var ri dynamic.ResourceInterface = client
	if ns := obj.GetNamespace(); ns != "" {
		ri = client.Namespace(ns)
	}

	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
		FieldManager: "plakar-k8s-exporter",
	})
	return obj, err
}

func (k *k8s) restoreConfig(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		if _, err := k.apply(ctx, record); err != nil {
			results <- record.Error(err)
			return err
		}

		results <- record.Ok()
	}

	return nil
}
