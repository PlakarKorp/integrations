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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
)

var applyTestResources = []*metav1.APIResourceList{
	{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "configmaps", Namespaced: true, Kind: "ConfigMap"},
			{Name: "namespaces", Namespaced: false, Kind: "Namespace"},
		},
	},
}

func newApplyTestK8s() *k8s {
	return &k8s{
		discover: &discoveryfake.FakeDiscovery{
			Fake: &clienttesting.Fake{Resources: applyTestResources},
		},
		dclient: dynamicfake.NewSimpleDynamicClient(scheme.Scheme),
	}
}

func fakeDynamic(k *k8s) *dynamicfake.FakeDynamicClient {
	return k.dclient.(*dynamicfake.FakeDynamicClient)
}

func recordFor(path, yaml string) *connectors.Record {
	return connectors.NewRecord(path, "", objects.FileInfo{Lmode: 0644}, nil,
		func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(yaml)), nil
		})
}

func runApply(t *testing.T, k *k8s, records ...*connectors.Record) ([]*connectors.Result, error) {
	t.Helper()

	in := make(chan *connectors.Record, len(records))
	out := make(chan *connectors.Result, len(records))
	for _, r := range records {
		in <- r
	}
	close(in)

	err := k.apply(t.Context(), in, out)
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

func TestApplySkipsNonRegularRecords(t *testing.T) {
	k := newApplyTestK8s()

	errRecord := connectors.NewError("/broken", errors.New("boom"))
	xattrRecord := connectors.NewXattr("/file", "user.foo", 0, func() (io.ReadCloser, error) {
		return nil, errors.New("should not be called")
	})
	dirRecord := connectors.NewRecord("/dir", "", objects.FileInfo{Lmode: fs.ModeDir | 0755}, nil,
		func() (io.ReadCloser, error) { return nil, errors.New("should not be called") })

	results, err := runApply(t, k, errRecord, xattrRecord, dirRecord)
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
	results, err := runApply(t, k, recordFor("/default/_/ConfigMap/v1/my-cm.yaml", yaml))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)

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
	results, err := runApply(t, k, recordFor("/_/_/Namespace/v1/my-ns.yaml", yaml))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)

	require.Empty(t, captured.namespace, "cluster-scoped resources must not be namespaced")
	require.Equal(t, "my-ns", captured.name)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("forced read failure") }
func (errReader) Close() error             { return nil }

func TestApplyDecodeError(t *testing.T) {
	k := newApplyTestK8s()

	record := connectors.NewRecord("/bad.yaml", "", objects.FileInfo{Lmode: 0644}, nil,
		func() (io.ReadCloser, error) { return errReader{}, nil })

	results, err := runApply(t, k, record)
	require.Error(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Err)
}

func TestApplyUnknownKind(t *testing.T) {
	k := newApplyTestK8s()

	yaml := `
apiVersion: acme.example.com/v1
kind: Widget
metadata:
  name: gizmo
`
	results, err := runApply(t, k, recordFor("/widget.yaml", yaml))
	require.Error(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Err)
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
	results, err := runApply(t, k, recordFor("/default/_/ConfigMap/v1/my-cm.yaml", yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "apiserver is on fire")
	require.Len(t, results, 1)
	require.Error(t, results[0].Err)
}
