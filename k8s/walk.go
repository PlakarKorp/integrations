package k8s

import (
	"bytes"
	"context"
	"io"
	"path"
	"slices"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

func (k *k8s) walkResources(ctx context.Context, records chan<- *connectors.Record) error {
	resources, err := k.discover.ServerPreferredResources()
	if err != nil {
		return err
	}

	var wg errgroup.Group
	wg.SetLimit(k.opts.MaxConcurrency)

	for _, resource := range resources {
		if err := ctx.Err(); err != nil {
			return err
		}

		groupVersion, err := schema.ParseGroupVersion(resource.GroupVersion)
		if err != nil {
			return err
		}

		for _, res := range resource.APIResources {
			// skip non-listable resources
			if !slices.Contains(res.Verbs, "list") {
				continue
			}

			if k.namespace != "" && !res.Namespaced {
				continue
			}

			gvr := groupVersion.WithResource(res.Name)

			wg.Go(func() error {
				list, err := k.dclient.Resource(gvr).List(ctx, metav1.ListOptions{
					LabelSelector: k.labels,
				})
				if err != nil {
					return err
				}

				for _, item := range list.Items {
					if item.GetLabels()["plakar.io/generated-resource"] == "true" {
						continue
					}

					byte, err := yaml.Marshal(item.Object)
					if err != nil {
						return err
					}

					var (
						gvk   = item.GroupVersionKind()
						group = "_"
						name  = item.GetName() + ".yaml"
						ns    = "_"
					)

					if res.Namespaced {
						ns = item.GetNamespace()
					}

					if k.namespace != "" && k.namespace != ns {
						continue
					}

					if gvk.Group != "" {
						group = gvk.Group
					}

					p := path.Join("/", ns, group, gvk.Kind, gvk.Version, name)

					finfo := objects.FileInfo{
						Lname:    name,
						Lsize:    int64(len(byte)),
						Lmode:    0644,
						LmodTime: item.GetCreationTimestamp().Time,
					}

					records <- connectors.NewRecord(p, "", finfo, nil,
						func() (io.ReadCloser, error) {
							return io.NopCloser(bytes.NewReader(byte)), nil
						})
				}

				return nil
			})
		}
	}

	return wg.Wait()
}
