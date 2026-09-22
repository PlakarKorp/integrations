package k8s

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
	yamlv3 "go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/restmapper"
	clienttesting "k8s.io/client-go/testing"
)

var applyTestResources = []*metav1.APIResourceList{
	{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "configmaps", Namespaced: true, Kind: "ConfigMap",
				Verbs: metav1.Verbs{"list", "get", "create", "patch", "delete"}},
			{Name: "namespaces", Namespaced: false, Kind: "Namespace",
				Verbs: metav1.Verbs{"list", "get", "create", "patch", "delete"}},
		},
	},
}

func newApplyTestK8s() *k8s {
	discover := &discoveryfake.FakeDiscovery{
		Fake: &clienttesting.Fake{Resources: applyTestResources},
	}
	discovercache := memory.NewMemCacheClientWithContext(discover)

	return &k8s{
		discover:      discover,
		discovercache: discovercache,
		mapper:        restmapper.NewDeferredDiscoveryRESTMapperWithContext(discovercache),
		dclient:       dynamicfake.NewSimpleDynamicClient(scheme.Scheme),
	}
}

func fakeDynamic(k *k8s) *dynamicfake.FakeDynamicClient {
	return k.dclient.(*dynamicfake.FakeDynamicClient)
}

func runRestoreConfig(t *testing.T, k *k8s, records ...*connectors.Record) ([]*connectors.Result, error) {
	t.Helper()

	in := make(chan *connectors.Record, len(records))
	out := make(chan *connectors.Result, len(records))
	for _, r := range records {
		in <- r
	}
	close(in)

	err := k.restoreConfig(t.Context(), in, out)
	close(out)

	var results []*connectors.Result
	for r := range out {
		results = append(results, r)
	}
	return results, err
}

// capturedApply holds what apply() sent for a resource. The fake dynamic
// client's own Apply can't merge unstructured objects (it needs Go struct
// tags for its strategic-merge-patch), so tests intercept the patch instead
// of round-tripping through it.
type capturedApply struct {
	namespace string
	name      string
	object    map[string]any
}

func interceptApply(dyn *dynamicfake.FakeDynamicClient, resource string) *capturedApply {
	c := &capturedApply{}
	dyn.PrependReactor("patch", resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		pa := action.(clienttesting.PatchAction)
		c.namespace = pa.GetNamespace()
		c.name = pa.GetName()
		_ = json.Unmarshal(pa.GetPatch(), &c.object)
		return true, &unstructured.Unstructured{Object: c.object}, nil
	})
	return c
}

func TestRestoreConfigSkipsNonRegularRecords(t *testing.T) {
	k := newApplyTestK8s()

	errRecord := connectors.NewError("/broken", errors.New("boom"))
	xattrRecord := connectors.NewXattr("/file", "user.foo", 0, func() (io.ReadCloser, error) {
		return nil, errors.New("should not be called")
	})
	dirRecord := connectors.NewRecord("/dir", "", objects.FileInfo{Lmode: fs.ModeDir | 0755}, nil,
		func() (io.ReadCloser, error) { return nil, errors.New("should not be called") })

	results, err := runRestoreConfig(t, k, errRecord, xattrRecord, dirRecord)
	require.NoError(t, err)
	require.Len(t, results, 3)
	for _, r := range results {
		require.NoError(t, r.Err, "non-regular records should be acked, not applied")
	}
}

func TestApplyNamespacedResource(t *testing.T) {
	k := newApplyTestK8s()
	captured := interceptApply(fakeDynamic(k), "configmaps")

	yaml := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
  uid: should-be-stripped
  managedFields:
    - manager: kubectl
data:
  foo: bar
`
	err := k.apply(t.Context(), "my-cm", strings.NewReader(yaml))
	require.NoError(t, err)

	require.Equal(t, "default", captured.namespace)
	require.Equal(t, "my-cm", captured.name)

	meta, _ := captured.object["metadata"].(map[string]any)
	require.NotContains(t, meta, "uid")
	require.NotContains(t, meta, "managedFields")
	data, _ := captured.object["data"].(map[string]any)
	require.Equal(t, "bar", data["foo"])
}

func TestApplyClusterScopedResource(t *testing.T) {
	k := newApplyTestK8s()
	captured := interceptApply(fakeDynamic(k), "namespaces")

	yaml := `
apiVersion: v1
kind: Namespace
metadata:
  name: my-ns
`
	err := k.apply(t.Context(), "my-ns", strings.NewReader(yaml))
	require.NoError(t, err)

	require.Empty(t, captured.namespace, "cluster-scoped resources must not be namespaced")
	require.Equal(t, "my-ns", captured.name)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("forced read failure") }
func (errReader) Close() error             { return nil }

func TestApplyDecodeError(t *testing.T) {
	k := newApplyTestK8s()

	err := k.apply(t.Context(), "err", errReader{})
	require.Error(t, err)
}

func TestApplyUnknownKind(t *testing.T) {
	k := newApplyTestK8s()

	yaml := `
apiVersion: acme.example.com/v1
kind: Widget
metadata:
  name: gizmo
`
	err := k.apply(t.Context(), "gizmo", strings.NewReader(yaml))
	require.Error(t, err)
}

func TestApplyServerError(t *testing.T) {
	k := newApplyTestK8s()
	fakeDynamic(k).PrependReactor("patch", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver is on fire")
	})

	yaml := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
`
	err := k.apply(t.Context(), "my-cm", strings.NewReader(yaml))
	require.ErrorContains(t, err, "apiserver is on fire")
}

func TestIsRestorable(t *testing.T) {
	suite := []struct {
		name  string
		verbs metav1.Verbs
		want  bool
	}{
		{
			name:  "a writable resource",
			verbs: metav1.Verbs{"get", "list", "watch", "create", "update", "patch", "delete"},
			want:  true,
		},
		{
			name:  "only the two verbs an apply needs",
			verbs: metav1.Verbs{"create", "patch"},
			want:  true,
		},
		{
			name:  "read-only endpoint",
			verbs: metav1.Verbs{"get", "list", "watch"},
			want:  false,
		},
		{
			name:  "create without patch, like the *reviews",
			verbs: metav1.Verbs{"create"},
			want:  false,
		},
		{
			name:  "patch without create",
			verbs: metav1.Verbs{"get", "list", "patch"},
			want:  false,
		},
		{
			name:  "no verbs advertised at all",
			verbs: nil,
			want:  false,
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, isRestorable(test.verbs))
		})
	}
}

func ownerRef(apiVersion, kind, name string, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		Controller: &controller,
	}
}

func TestSkipRestore(t *testing.T) {
	suite := []struct {
		name    string
		gvk     schema.GroupVersionKind
		owners  []metav1.OwnerReference
		skipped []schema.GroupKind
		skip    bool
	}{
		{
			name: "core kind on the list",
			gvk:  schema.GroupVersionKind{Version: "v1", Kind: "Node"},
			skip: true,
		},
		{
			name: "grouped kind on the list",
			gvk:  schema.GroupVersionKind{Group: "storage.k8s.io", Version: "v1", Kind: "VolumeAttachment"},
			skip: true,
		},
		{
			name: "the version is not part of the match",
			gvk:  schema.GroupVersionKind{Group: "events.k8s.io", Version: "v1beta1", Kind: "Event"},
			skip: true,
		},
		{
			name: "ordinary resource",
			gvk:  schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"},
			skip: false,
		},
		{
			name: "same kind in a group we do not disallow",
			gvk:  schema.GroupVersionKind{Group: "acme.example.com", Version: "v1", Kind: "Node"},
			skip: false,
		},
		{
			name:    "kind skipped in the configuration",
			gvk:     schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			skipped: []schema.GroupKind{{Group: "apps", Kind: "Deployment"}},
			skip:    true,
		},
		{
			name:    "the version is not part of the configured match",
			gvk:     schema.GroupVersionKind{Group: "apps", Version: "v1beta1", Kind: "Deployment"},
			skipped: []schema.GroupKind{{Group: "apps", Kind: "Deployment"}},
			skip:    true,
		},
		{
			name:    "another kind of a configured group",
			gvk:     schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"},
			skipped: []schema.GroupKind{{Group: "apps", Kind: "Deployment"}},
			skip:    false,
		},
		{
			name:   "controller-owned resource",
			gvk:    schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"},
			owners: []metav1.OwnerReference{ownerRef("apps/v1", "Deployment", "nginx", true)},
			skip:   true,
		},
		{
			name: "only one of the owners controls it",
			gvk:  schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
			owners: []metav1.OwnerReference{
				ownerRef("acme.example.com/v1", "Widget", "w", false),
				ownerRef("apps/v1", "ReplicaSet", "nginx-abc", true),
			},
			skip: true,
		},
		{
			name:   "a plain owner reference is not a controller",
			gvk:    schema.GroupVersionKind{Version: "v1", Kind: "Secret"},
			owners: []metav1.OwnerReference{ownerRef("acme.example.com/v1", "Widget", "w", false)},
			skip:   false,
		},
		{
			name:   "an owner reference without a controller flag",
			gvk:    schema.GroupVersionKind{Version: "v1", Kind: "Secret"},
			owners: []metav1.OwnerReference{{APIVersion: "acme.example.com/v1", Kind: "Widget", Name: "w"}},
			skip:   false,
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			ignored := make(Filters)
			for _, gk := range test.skipped {
				ignored[gk] = defaultFilter
			}

			k := &k8s{restoreFilters: mergeMaps(ignored, neverRestore)}

			meta := metav1.ObjectMeta{OwnerReferences: test.owners}

			reason, err := k.skipRestore(test.gvk, meta)
			require.NoError(t, err)
			if !test.skip {
				require.Empty(t, reason)
				return
			}
			require.NotEmpty(t, reason, "a skipped kind must say why")
		})
	}
}

func TestSkipRestoreFilters(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	gk := gvk.GroupKind()

	t.Run("restores what the filter keeps", func(t *testing.T) {
		k := &k8s{restoreFilters: Filters{gk: func(meta metav1.ObjectMeta) (bool, error) {
			return meta.Labels["restore"] == "yes", nil
		}}}

		reason, err := k.skipRestore(gvk, metav1.ObjectMeta{
			Name:   "nginx",
			Labels: map[string]string{"restore": "yes"},
		})
		require.NoError(t, err)
		require.Empty(t, reason)
	})

	t.Run("skips what the filter drops", func(t *testing.T) {
		k := &k8s{restoreFilters: Filters{gk: defaultFilter}}

		reason, err := k.skipRestore(gvk, metav1.ObjectMeta{Name: "nginx"})
		require.NoError(t, err)
		require.NotEmpty(t, reason, "a filtered kind must say why")
	})

	t.Run("leaves other kinds alone", func(t *testing.T) {
		k := &k8s{restoreFilters: Filters{{Group: "apps", Kind: "StatefulSet"}: defaultFilter}}

		reason, err := k.skipRestore(gvk, metav1.ObjectMeta{Name: "nginx"})
		require.NoError(t, err)
		require.Empty(t, reason)
	})

	t.Run("yields the filter error", func(t *testing.T) {
		k := &k8s{restoreFilters: Filters{gk: func(metav1.ObjectMeta) (bool, error) {
			return false, errors.New("boom")
		}}}

		_, err := k.skipRestore(gvk, metav1.ObjectMeta{Name: "nginx"})
		require.ErrorContains(t, err, "boom")
	})
}

func TestObjectMeta(t *testing.T) {
	decode := func(t *testing.T, doc string) *unstructured.Unstructured {
		t.Helper()
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		require.NoError(t, yamlv3.NewDecoder(strings.NewReader(doc)).Decode(&obj.Object))
		return obj
	}

	t.Run("decodes the metadata into the typed struct", func(t *testing.T) {
		obj := decode(t, `
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: nginx-abc
  namespace: default
  generation: 3
  creationTimestamp: 2024-01-01T00:00:00Z
  labels:
    app: nginx
  ownerReferences:
    - apiVersion: apps/v1
      kind: Deployment
      name: nginx
      controller: true
spec:
  replicas: 2
`)

		meta, err := objectMeta(obj)
		require.NoError(t, err)
		require.Equal(t, "nginx-abc", meta.Name)
		require.Equal(t, "default", meta.Namespace)
		require.EqualValues(t, 3, meta.Generation)
		require.Equal(t, map[string]string{"app": "nginx"}, meta.Labels)
		require.False(t, meta.CreationTimestamp.IsZero())
		require.Len(t, meta.OwnerReferences, 1)
		require.NotNil(t, controllerOf(meta.OwnerReferences))
	})

	t.Run("does not alter the object it reads", func(t *testing.T) {
		obj := decode(t, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
data:
  foo: bar
`)

		meta, err := objectMeta(obj)
		require.NoError(t, err)
		meta.Name = "other"

		require.Equal(t, "my-cm", obj.GetName())
		require.NotContains(t, obj.Object["metadata"], "creationTimestamp")
	})

	t.Run("reports an object without metadata", func(t *testing.T) {
		obj := decode(t, "apiVersion: v1\nkind: ConfigMap\n")

		_, err := objectMeta(obj)
		require.Error(t, err)
	})
}
