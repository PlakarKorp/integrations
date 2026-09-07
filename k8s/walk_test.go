package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
)

// stubDiscovery overrides ServerPreferredResourcesWithContext, since
// discoveryfake.FakeDiscovery's own implementation always returns (nil, nil)
// regardless of its Resources field.
type stubDiscovery struct {
	*discoveryfake.FakeDiscovery
	resources []*metav1.APIResourceList
	err       error
}

func (s *stubDiscovery) ServerPreferredResourcesWithContext(context.Context) ([]*metav1.APIResourceList, error) {
	return s.resources, s.err
}

func newWalkTestK8s(namespace string, resources []*metav1.APIResourceList, objs ...runtime.Object) *k8s {
	return &k8s{
		namespace: namespace,
		discover: &stubDiscovery{
			FakeDiscovery: &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}},
			resources:     resources,
		},
		dclient: dynamicfake.NewSimpleDynamicClient(scheme.Scheme, objs...),
		opts:    &connectors.Options{MaxConcurrency: 4},
	}
}

func podObj(ns, name string, generated bool) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": ns}
	if generated {
		meta["labels"] = map[string]any{"plakar.io/generated-resource": "true"}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   meta,
	}}
}

func namespaceObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}}
}

func runWalk(t *testing.T, k *k8s) ([]*connectors.Record, error) {
	t.Helper()

	records := make(chan *connectors.Record)
	var got []*connectors.Record
	done := make(chan struct{})
	go func() {
		defer close(done)
		for r := range records {
			got = append(got, r)
		}
	}()

	err := k.walkResources(t.Context(), records)
	close(records)
	<-done
	return got, err
}

func paths(records []*connectors.Record) []string {
	var p []string
	for _, r := range records {
		p = append(p, r.Pathname)
	}
	return p
}

var podResources = []*metav1.APIResourceList{
	{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}},
			{Name: "componentstatuses", Kind: "ComponentStatus", Namespaced: false, Verbs: metav1.Verbs{"get"}},
			{Name: "namespaces", Kind: "Namespace", Namespaced: false, Verbs: metav1.Verbs{"list"}},
		},
	},
}

func TestWalkResourcesClusterWide(t *testing.T) {
	k := newWalkTestK8s("", podResources,
		podObj("default", "web-1", false),
		podObj("default", "web-2", true), // generated, must be skipped
		podObj("other", "other-1", false),
		namespaceObj("prod"),
	)

	records, err := runWalk(t, k)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"/default/_/Pod/v1/web-1.yaml",
		"/other/_/Pod/v1/other-1.yaml",
		"/_/_/Namespace/v1/prod.yaml",
	}, paths(records))
}

func TestWalkResourcesNamespaceFiltering(t *testing.T) {
	k := newWalkTestK8s("default", podResources,
		podObj("default", "web-1", false),
		podObj("other", "other-1", false),
		namespaceObj("prod"),
	)

	records, err := runWalk(t, k)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"/default/_/Pod/v1/web-1.yaml"}, paths(records))
}

func withListError(k *k8s, resource string, err error) {
	dyn := k.dclient.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("list", resource, func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

// podOnlyResources reuses the well-known "pods" GVR (rather than a made-up
// one) since the fake dynamic client panics on List() for any resource it
// has no registered list-kind for.
var podOnlyResources = []*metav1.APIResourceList{
	{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list"}},
		},
	},
}

func TestWalkResourcesRecordsForbiddenAsError(t *testing.T) {
	k := newWalkTestK8s("", podOnlyResources)
	withListError(k, "pods", apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("nope")))

	records, err := runWalk(t, k)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "/_/_/Pod/v1", records[0].Pathname)
	require.Error(t, records[0].Err)
}

func TestWalkResourcesSkipsNotFound(t *testing.T) {
	k := newWalkTestK8s("", podOnlyResources)
	withListError(k, "pods", apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, ""))

	records, err := runWalk(t, k)
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestWalkResourcesAbortsOnUnauthorized(t *testing.T) {
	k := newWalkTestK8s("", podOnlyResources)
	withListError(k, "pods", apierrors.NewUnauthorized("bad creds"))

	records, err := runWalk(t, k)
	require.Error(t, err)
	require.Empty(t, records)
}

func TestWalkResourcesDiscoveryError(t *testing.T) {
	k := newWalkTestK8s("", nil)
	k.discover.(*stubDiscovery).err = errors.New("discovery is down")

	_, err := runWalk(t, k)
	require.ErrorContains(t, err, "discovery is down")
}

func TestWalkResourcesBadGroupVersion(t *testing.T) {
	k := newWalkTestK8s("", []*metav1.APIResourceList{{GroupVersion: "a/b/c"}})

	_, err := runWalk(t, k)
	require.Error(t, err)
}

func TestClassifyListErrorContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, walkAbort, classifyListError(ctx, errors.New("whatever")))
}
