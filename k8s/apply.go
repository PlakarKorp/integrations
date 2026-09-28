package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	yamlv3 "go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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
	{Group: "", Kind: "PersistentVolume"}:               defaultFilter,
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

// unboundAnnotations removes the annotations that binds the PVC to
// its PV.  Returns a new map.
func unboundAnnotations(annotations map[string]string) map[string]string {
	annotations = maps.Clone(annotations)
	maps.DeleteFunc(annotations, func(key, _ string) bool {
		return strings.HasPrefix(key, "pv.kubernetes.io/") ||
			strings.HasPrefix(key, "volume.kubernetes.io/") ||
			strings.HasPrefix(key, "volume.beta.kubernetes.io/")
	})

	if len(annotations) == 0 {
		return nil
	}
	return annotations
}

// restorable drops the source cluster-specific info that don't make
// sense when restoring, or that might fail alltogether.
func restorable(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")

	// the resource version is a precondition for the apply: keeping
	// the one from the backup makes every restore over an existing
	// object fail with a conflict.
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")

	switch obj.GroupVersionKind() {
	case schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}:
		// volumeName references a PV on the old cluster,
		// cannot be restored, otherwise it'll come up as
		// "Lost".
		unstructured.RemoveNestedField(obj.Object, "spec", "volumeName")
		obj.SetAnnotations(unboundAnnotations(obj.GetAnnotations()))

	case schema.GroupVersionKind{Version: "v1", Kind: "Service"}:
		// drop the cluster IP since it might not be
		// re-allocated as-is.  None is not an address though,
		// so keep it.
		ip, _, _ := unstructured.NestedString(obj.Object, "spec", "clusterIP")
		if ip == corev1.ClusterIPNone {
			return
		}
		unstructured.RemoveNestedField(obj.Object, "spec", "clusterIP")
		unstructured.RemoveNestedField(obj.Object, "spec", "clusterIPs")
	}
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

	restorable(obj)

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

	if k.namespace != "" {
		// cannot map cluster-scoped resource to namespace
		if rest.Scope.Name() != apimeta.RESTScopeNameNamespace {
			log.Printf("skipping %s: %s is cluster-scoped and we're restoring into %s",
				name, gvk.Kind, k.namespace)
			return nil
		}

		k.warnCollapse(gvk.GroupKind(), meta)
		obj.SetNamespace(k.namespace)
	}

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

// remapKey identifies an object regardless of the namespace it was
// backed up from.
type remapKey struct {
	gk   schema.GroupKind
	name string
}

// warnCollapse warns when two objects from different namespaces are
// remapped onto the same name: the second apply silently overwrites the
// first one.
func (k *k8s) warnCollapse(gk schema.GroupKind, meta metav1.ObjectMeta) {
	key := remapKey{gk: gk, name: meta.Name}

	if prev, ok := k.remapped[key]; ok && prev != meta.Namespace {
		log.Printf("%s %s from namespace %s overwrites the one from %s: both are restored into %s",
			gk.Kind, meta.Name, meta.Namespace, prev, k.namespace)
	}

	k.remapped[key] = meta.Namespace
}

// ensureNamespace creates the namespace we're restoring into.  it's a
// best effort, the namespace might already be there or we might not
// be allowed to create it.
func (k *k8s) ensureNamespace(ctx context.Context, ns string) {
	obj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}

	_, err := k.clientset.CoreV1().Namespaces().Create(ctx, obj, metav1.CreateOptions{})
	switch {
	case err == nil:
		log.Printf("created namespace %s", ns)
	case apierrors.IsAlreadyExists(err):
		// nothing
	default:
		// we'll fail later anyway
		log.Printf("failed to create namespace %s: %s", ns, err)
	}
}

func (k *k8s) restoreConfig(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	if k.namespace != "" {
		k.ensureNamespace(ctx, k.namespace)
	}

	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		if err := k.apply(ctx, record.Pathname, record.Reader); err != nil {
			results <- record.Error(err)
			continue
		}

		results <- record.Ok()
	}

	return nil
}
