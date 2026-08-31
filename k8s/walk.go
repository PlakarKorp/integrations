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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

type walkAction int

const (
	walkRecord walkAction = iota // report the type as failed in the snapshot
	walkSkip                     // resource deleted concurrently; safe to skip
	walkAbort                    // stop the world
)

func classifyListError(ctx context.Context, err error) walkAction {
	// let's first handle the context cancelled / deadline case.
	if ctx.Err() != nil {
		return walkAbort
	}

	switch {
	// lost the race, got something during the resource list that
	// disappearead afterward.  safe to skip, nothing much to do.
	case apierrors.IsNotFound(err), apierrors.IsMethodNotSupported(err):
		return walkSkip

	// our authentication got revoked, so every other call will
	// likely fail the same.
	case apierrors.IsUnauthorized(err):
		return walkAbort
	}

	// forbidden, etc ..., these needs to be recorded.
	return walkRecord
}

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
					switch classifyListError(ctx, err) {
					case walkRecord:
						var (
							ns    = "_"
							group = "_"
						)
						if k.namespace != "" {
							ns = k.namespace
						}
						if groupVersion.Group != "" {
							group = groupVersion.Group
						}
						p := path.Join("/", ns, group, res.Kind,
							groupVersion.Version)
						records <- connectors.NewError(p, err)
						return nil
					case walkSkip:
						return nil
					default:
						return err
					}
				}

				for _, item := range list.Items {
					if item.GetLabels()["plakar.io/generated-resource"] == "true" {
						continue
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

					byte, err := yaml.Marshal(item.Object)
					if err != nil {
						records <- connectors.NewError(p, err)
						continue
					}

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
