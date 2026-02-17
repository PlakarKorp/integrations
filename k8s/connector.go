package k8s

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type k8s struct {
	proto      string
	config     *rest.Config
	clientset  *kubernetes.Clientset
	dclient    *dynamic.DynamicClient
	discover   *discovery.DiscoveryClient
	snapClient *versioned.Clientset
	opts       *connectors.Options

	host      string
	namespace string
	pvcName   string

	portForward bool

	volumeSnapshotClass string
	kubeletImage        string
}

func init() {
	importer.Register("k8s", 0, NewImporter)
	importer.Register("k8s+pvc", 0, NewImporter)

	exporter.Register("k8s", 0, NewExporter)
	exporter.Register("k8s+pvc", 0, NewExporter)
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (importer.Importer, error) {
	return New(ctx, opts, name, params, false)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, name, params, true)
}

func New(ctx context.Context, opts *connectors.Options, proto string, params map[string]string, export bool) (*k8s, error) {
	var host string

	u, err := url.Parse(params["location"])
	if err != nil {
		return nil, fmt.Errorf("bad location: %w", err)
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

	var namespace, pvcName, snapClass string

	switch proto {
	case "k8s+pvc":
		var found bool

		namespace, pvcName, found = strings.Cut(strings.Trim(u.Path, "/"), "/")
		if !found || strings.Contains(pvcName, "/") {
			return nil, fmt.Errorf("bad location: expected namespace/pvc-name but got %s",
				strings.Trim(u.Path, "/"))
		}

		snapClass = params["volume_snapshot_class"]
		if snapClass == "" && !export {
			return nil, fmt.Errorf("missing volume_snapshot_class option")
		}

	case "k8s":
		namespace = strings.Trim(u.Path, "/")
		if strings.Contains(namespace, "/") {
			return nil, fmt.Errorf("bad location: slashes in namespace: %s", params["location"])
		}

	default:
		return nil, fmt.Errorf("integration-k8s cannot handle protocol %s", proto)
	}

	kubeletImage := params["kubelet_image"]
	if kubeletImage == "" {
		kubeletImage = "ghcr.io/plakarkorp/kubelet:328f8feb-202602161900"
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

	snapClient, err := versioned.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &k8s{
		proto:      proto,
		config:     config,
		clientset:  clientset,
		dclient:    dclient,
		discover:   discover,
		snapClient: snapClient,
		opts:       opts,
		host:       host,
		namespace:  namespace,
		pvcName:    pvcName,

		portForward: true,

		volumeSnapshotClass: snapClass,
		kubeletImage:        kubeletImage,
	}, nil
}

func (k *k8s) Type() string   { return k.proto }
func (k *k8s) Origin() string { return k.host }

func (k *k8s) Root() string {
	if k.proto == "k8s+pvc" {
		return "/"
	}
	return "/" + k.namespace
}

func (k *k8s) Flags() location.Flags {
	if k.proto == "k8s+pvc" {
		return location.FLAG_STREAM | location.FLAG_NEEDACK
	}
	return 0
}

func (k *k8s) Ping(ctx context.Context) error {
	ns := k.clientset.CoreV1().Namespaces()
	_, err := ns.Get(ctx, "default", metav1.GetOptions{})
	return err
}

func (k *k8s) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	switch k.proto {
	case "k8s":
		return k.walkResources(ctx, records)
	case "k8s+pvc":
		return k.backupPvc(ctx, k.namespace, k.pvcName, records, results)
	default:
		return errors.ErrUnsupported
	}
}

func (k *k8s) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	switch k.proto {
	case "k8s":
		defer close(results)
		return k.apply(ctx, records, results)
	case "k8s+pvc":
		// no need to close results here, it's passed to
		// exporter.Export which will take care of it.
		return k.restorePvc(ctx, k.namespace, k.pvcName, records, results)
	default:
		return errors.ErrUnsupported
	}
}

func (k *k8s) Close(ctx context.Context) error {
	return nil
}
