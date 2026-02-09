package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	yamlv3 "go.yaml.in/yaml/v3"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

type k8s struct {
	clientset *kubernetes.Clientset
	dclient   *dynamic.DynamicClient
	discover  *discovery.DiscoveryClient
	opts      *connectors.Options

	host string
	root string
}

func init() {
	importer.Register("k8s", 0, NewImporter)
	exporter.Register("k8s", 0, NewExporter)
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (importer.Importer, error) {
	return New(ctx, opts, name, params)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, name, params)
}

func New(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (*k8s, error) {
	var host, root string

	u, err := url.Parse(params["location"])
	if err != nil {
		return nil, fmt.Errorf("bad location: %w", err)
	}

	root = u.Path
	if !strings.HasPrefix(root, "/") {
		root = "/" + root
	}

	var config *rest.Config
	if u.Host != "" {
		config = &rest.Config{
			Host: u.Host,
		}
		host = u.Host
	} else {
		var err error
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("%w (not running in a kubernetes cluster?)", err)
		}
		host = "in-cluster"
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	dclient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	discover, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	return &k8s{
		clientset: clientset,
		dclient:   dclient,
		discover:  discover,
		opts:      opts,
		host:      host,
		root:      root,
	}, nil
}

func (k *k8s) Type() string          { return "k8s" }
func (k *k8s) Origin() string        { return k.host }
func (k *k8s) Root() string          { return k.root }
func (k *k8s) Flags() location.Flags { return 0 }

func (k *k8s) Ping(ctx context.Context) error {
	ns := k.clientset.CoreV1().Namespaces()
	_, err := ns.Get(ctx, "default", metav1.GetOptions{})
	return err
}

func (k *k8s) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

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

			gvr := groupVersion.WithResource(res.Name)

			wg.Go(func() error {
				list, err := k.dclient.Resource(gvr).List(ctx, metav1.ListOptions{})
				if err != nil {
					return err
				}

				for _, item := range list.Items {
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

func (k *k8s) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

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
			return err
		}

		if meta, ok := obj.Object["metadata"].(map[string]any); ok {
			delete(meta, "managedFields")
			delete(meta, "uid")
		}

		gvk := obj.GroupVersionKind()
		rest, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
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
			return err
		}
	}

	return nil
}

func (k *k8s) Close(ctx context.Context) error {
	return nil
}
