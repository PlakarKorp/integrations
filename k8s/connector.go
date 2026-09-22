package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"kubevirt.io/client-go/kubevirt"
)

type k8s struct {
	proto          string
	config         *rest.Config
	clientset      kubernetes.Interface
	dclient        dynamic.Interface
	discover       discovery.DiscoveryInterfaceWithContext
	discovercache  discovery.CachedDiscoveryInterfaceWithContext
	mapper         *restmapper.DeferredDiscoveryRESTMapper
	snapClient     versioned.Interface
	kubevirtClient kubevirt.Interface
	opts           *connectors.Options
	export         bool

	host      string
	namespace string
	labels    string
	pvcName   string
	vmName    string

	portForward bool

	volumeSnapshotClass    string
	kubeletImage           string
	kubeletImagePullPolicy corev1.PullPolicy
	kubeletCapas           []corev1.Capability

	restoreFilters Filters
}

func init() {
	importer.Register("k8s", 0, NewImporter)
	importer.Register("k8s+csi", 0, NewImporter)
	importer.Register("k8s+pvc", 0, NewImporter)
	importer.Register("k8s+vm", 0, NewImporter)

	exporter.Register("k8s", 0, NewExporter)
	exporter.Register("k8s+pvc", 0, NewExporter)
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (importer.Importer, error) {
	if params["fs_access"] == "" {
		params["fs_access"] = "read"
	}
	return New(ctx, opts, name, params, false)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (exporter.Exporter, error) {
	if params["fs_access"] == "" {
		params["fs_access"] = "full"
	}
	return New(ctx, opts, name, params, true)
}

// Filter reports whether an object of a given group/kind must be restored.
type Filter func(meta metav1.ObjectMeta) (bool, error)
type Filters map[schema.GroupKind]Filter

// defaultFilter never restores.
var defaultFilter = func(_ metav1.ObjectMeta) (bool, error) {
	return false, nil
}

type options struct {
	filters Filters
}

func newOptions() *options {
	return &options{}
}

type Options func(o *options)

func WithFilters(filters Filters) Options {
	return func(o *options) {
		o.filters = filters
	}
}

func New(
	ctx context.Context,
	opts *connectors.Options,
	proto string,
	params map[string]string,
	export bool,
	k8sOptions ...Options,
) (*k8s, error) {
	k8sOpts := newOptions()
	for _, f := range k8sOptions {
		f(k8sOpts)
	}

	var host string
	var portForward bool
	var hasKubeConfig bool

	u, err := url.Parse(params["location"])
	if err != nil {
		return nil, fmt.Errorf("bad location: %w", err)
	}

	home, _ := os.UserHomeDir()
	kubeconfpath := filepath.Join(home, ".kube", "config")
	if v, ok := params["kubeconfig_file"]; ok {
		hasKubeConfig = true
		kubeconfpath = v
	}

	var kubeconf []byte
	if content, ok := params["kubeconfig"]; ok {
		kubeconf = []byte(content)
	} else {
		kubeconf, err = os.ReadFile(kubeconfpath)
		if err != nil {
			if hasKubeConfig {
				return nil, fmt.Errorf("failed to open %s: %w",
					kubeconfpath, err)
			}
		}
	}

	var config *rest.Config
	if kubeconf != nil {
		config, err = clientcmd.RESTConfigFromKubeConfig(kubeconf)
		if err != nil {
			return nil, fmt.Errorf("failed to create config from kubeconfig: %w", err)
		}
		portForward = true

		if u, err := url.Parse(config.Host); err == nil {
			host = u.Host
		}
	} else if u.Host != "" {
		config = &rest.Config{
			Host: u.Host,
		}
		host = u.Host
		portForward = true
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("%w (not running in a kubernetes cluster?)", err)
		}
		host = "in-cluster"
	}

	var namespace, pvcName, vmName, matchLabels, snapClass string

	switch proto {
	case "k8s+csi":
		if export {
			return nil, fmt.Errorf("k8s+csi is for importers only; use k8s+pvc for restore")
		}

		snapClass = params["volume_snapshot_class"]
		if snapClass == "" && !export {
			return nil, fmt.Errorf("missing volume_snapshot_class option")
		}

		fallthrough
	case "k8s+pvc":
		var found bool
		namespace, pvcName, found = strings.Cut(strings.Trim(u.Path, "/"), "/")
		if !found || strings.Contains(pvcName, "/") {
			return nil, fmt.Errorf("bad location: expected namespace/pvc-name but got %s",
				strings.Trim(u.Path, "/"))
		}

	case "k8s+vm":
		var found bool
		namespace, vmName, found = strings.Cut(strings.Trim(u.Path, "/"), "/")
		if !found || strings.Contains(vmName, "/") {
			return nil, fmt.Errorf("bad location: expected namespace/vm-name but got %s",
				strings.Trim(u.Path, "/"))
		}

	case "k8s":
		namespace = strings.Trim(u.Path, "/")
		if strings.Contains(namespace, "/") {
			return nil, fmt.Errorf("bad location: slashes in namespace: %s", params["location"])
		}

		if l, ok := params["labels"]; ok && !export {
			_, err := labels.Parse(l)
			if err != nil {
				return nil, fmt.Errorf("failed to parse labels: %w", err)
			}
			matchLabels = l
		}

	default:
		return nil, fmt.Errorf("integrations/k8s cannot handle protocol %s", proto)
	}

	kubeletImage := params["kubelet_image"]
	if kubeletImage == "" {
		kubeletImage = "ghcr.io/plakarkorp/kubelet:541eeddc56949fc617da602d3bce234592108371-34600504204"
	}

	var capas []corev1.Capability
	switch c := params["fs_access"]; c {
	case "", "default":
	case "read":
		capas = []corev1.Capability{"DAC_READ_SEARCH"}
	case "full":
		capas = []corev1.Capability{"DAC_OVERRIDE", "CHOWN", "FOWNER", "FSETID"}
	default:
		return nil, fmt.Errorf("bad fs_access %q: expected default, read or full", c)
	}

	ignoreResources, err := parseIgnoreResources(params["ignore_resources"])
	if err != nil {
		return nil, err
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

	kubevirtClient, err := kubevirt.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	discovercache := memory.NewMemCacheClientWithContext(discover)
	mapper := restmapper.NewDeferredDiscoveryRESTMapperWithContext(
		discovercache,
	)

	return &k8s{
		proto:          proto,
		config:         config,
		clientset:      clientset,
		dclient:        dclient,
		discover:       discover,
		discovercache:  discovercache,
		mapper:         mapper,
		snapClient:     snapClient,
		kubevirtClient: kubevirtClient,
		opts:           opts,
		export:         export,
		host:           host,
		namespace:      namespace,
		labels:         matchLabels,
		pvcName:        pvcName,
		vmName:         vmName,

		portForward: portForward,

		restoreFilters:      mergeMaps(neverRestore, k8sOpts.filters, ignoreResources),
		volumeSnapshotClass: snapClass,
		kubeletImage:        kubeletImage,
		kubeletCapas:        capas,
	}, nil
}

// mergeMaps merges maps, later maps can override duplicates
func mergeMaps[K comparable, V any](ms ...map[K]V) map[K]V {
	newMap := make(map[K]V)
	for _, m := range ms {
		maps.Copy(newMap, m)
	}
	return newMap
}

func parseIgnoreResources(str string) (Filters, error) {
	gks := make(Filters)
	gksStrs := strings.SplitSeq(str, ";")
	for gkStr := range gksStrs {
		if gkStr == "" {
			continue
		}
		gk, err := parseGroupKind(gkStr)
		if err != nil {
			return nil, err
		}
		if _, ok := gks[gk]; ok {
			log.Printf("duplicate group/kind: %s/%s", gk.Group, gk.Kind)
		}
		gks[gk] = defaultFilter
	}
	return gks, nil
}

func parseGroupKind(str string) (schema.GroupKind, error) {
	g, k, ok := strings.Cut(str, "/")
	if !ok {
		return schema.GroupKind{}, fmt.Errorf("invalid format for group/kind %q", str)
	}
	if err := validateGroup(g); err != nil {
		return schema.GroupKind{}, err
	}
	if err := validateKind(k); err != nil {
		return schema.GroupKind{}, err
	}
	return schema.GroupKind{
		Group: g,
		Kind:  k,
	}, nil
}

var kindRegexp = regexp.MustCompile("^[A-Za-z0-9-]+$")

func validateKind(kind string) error {
	if !kindRegexp.MatchString(kind) {
		return fmt.Errorf("invalid format for kind %q", kind)
	}
	return nil
}

var groupRegexp = regexp.MustCompile("^[a-z0-9.-]*$")

func validateGroup(group string) error {
	if !groupRegexp.MatchString(group) {
		return fmt.Errorf("invalid format for group %q", group)
	}
	return nil
}

func (k *k8s) Type() string   { return k.proto }
func (k *k8s) Origin() string { return k.host }

func (k *k8s) Root() string {
	if k.proto == "k8s+csi" || k.proto == "k8s+pvc" || k.proto == "k8s+vm" {
		return "/"
	}
	return "/" + k.namespace
}

func (k *k8s) Flags() location.Flags {
	if k.proto == "k8s+csi" || k.proto == "k8s+pvc" || k.proto == "k8s+vm" {
		return location.FLAG_STREAM | location.FLAG_NEEDACK
	}
	return 0
}

func (k *k8s) Ping(ctx context.Context) error {
	switch k.proto {
	case "k8s":
		verb := "list"
		if k.export {
			verb = "patch"
		}

		ssar := &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authv1.ResourceAttributes{
					// if "" it's cluster-wide, which is intended
					Namespace: k.namespace,
					Verb:      verb,
					Group:     "apps",
					Resource:  "deployments",
				},
			},
		}
		res, err := k.clientset.AuthorizationV1().SelfSubjectAccessReviews().
			Create(ctx, ssar, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if !res.Status.Allowed {
			return fmt.Errorf("not allowed to %s deployments in %q: %s",
				verb, k.namespace, res.Status.Reason)
		}
		return nil
	case "k8s+csi", "k8s+pvc":
		_, err := k.getpvc(ctx, k.namespace, k.pvcName)
		return err
	case "k8s+vm":
		_, err := k.getvm(ctx, k.namespace, k.vmName)
		return err
	default:
		return errors.ErrUnsupported
	}
}

func (k *k8s) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	switch k.proto {
	case "k8s":
		return k.walkResources(ctx, records)
	case "k8s+pvc", "k8s+csi":
		return k.backupPvc(ctx, k.namespace, k.pvcName, records, results)
	case "k8s+vm":
		return k.backupVM(ctx, k.namespace, k.vmName, records, results)
	default:
		return errors.ErrUnsupported
	}
}

func (k *k8s) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	switch k.proto {
	case "k8s":
		defer close(results)
		return k.restoreConfig(ctx, records, results)
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
