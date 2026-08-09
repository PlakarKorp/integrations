package k8s

import (
	"context"

	"github.com/PlakarKorp/kloset/connectors"
	yamlv3 "go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

func (k *k8s) apply(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	var (
		discovery = memory.NewMemCacheClient(k.discover)
		mapper    = restmapper.NewDeferredDiscoveryRESTMapper(discovery)
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

		if meta, ok := obj.Object["metadata"].(map[string]any); ok {
			delete(meta, "managedFields")
			delete(meta, "uid")
		}

		gvk := obj.GroupVersionKind()
		rest, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			results <- record.Error(err)
			return err
		}

		gvr := rest.Resource

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
