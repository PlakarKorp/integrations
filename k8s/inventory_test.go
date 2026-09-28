package k8s

import (
	"errors"
	"testing"

	sdk "github.com/PlakarKorp/go-inventory-sdk/inventory"
	"github.com/PlakarKorp/pkg"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

const testClusterUID = "cafe-1234"

func newInventoryTest(objs ...runtime.Object) *inventory {
	kubeSystem := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: testClusterUID},
	}
	objs = append([]runtime.Object{kubeSystem}, objs...)
	return &inventory{clientset: k8sfake.NewSimpleClientset(objs...), namespaces: []string{""}}
}

func pvcObj(ns, name string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func runInventoryList(t *testing.T, inv *inventory) ([]*sdk.InventoryEntry, error) {
	t.Helper()

	entries := make(chan *sdk.InventoryEntry)
	var got []*sdk.InventoryEntry
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range entries {
			got = append(got, e)
		}
	}()

	err := inv.List(t.Context(), entries)
	<-done
	return got, err
}

func TestInventoryListPVCs(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest(pvcObj("ns1", "pvc1"), pvcObj("ns2", "pvc2"))

	entries, err := runInventoryList(t, inv)
	require.NoError(t, err)
	require.ElementsMatch(t, []*sdk.InventoryEntry{
		clusterEntry(),
		{
			Class:     pkg.ResourceClassBlockStorage,
			SubClass:  pkg.ResourceSubClassPVC,
			URN:       "k8s:ns1:pvc1",
			Name:      "pvc1",
			Endpoints: []sdk.HostEndpoint{{Type: sdk.EndpointIdentifier, Endpoint: "/ns1/pvc1"}},
		},
		{
			Class:     pkg.ResourceClassBlockStorage,
			SubClass:  pkg.ResourceSubClassPVC,
			URN:       "k8s:ns2:pvc2",
			Name:      "pvc2",
			Endpoints: []sdk.HostEndpoint{{Type: sdk.EndpointIdentifier, Endpoint: "/ns2/pvc2"}},
		},
	}, entries)
}

func TestInventoryListPVCsFollowsContinueToken(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest()
	clientset := inv.clientset.(*k8sfake.Clientset)

	calls := 0
	clientset.PrependReactor("list", "persistentvolumeclaims", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		list := &corev1.PersistentVolumeClaimList{}
		if calls == 1 {
			list.Items = []corev1.PersistentVolumeClaim{*pvcObj("ns1", "pvc1")}
			list.Continue = "page2"
		} else {
			list.Items = []corev1.PersistentVolumeClaim{*pvcObj("ns2", "pvc2")}
		}
		return true, list, nil
	})

	entries, err := runInventoryList(t, inv)
	require.NoError(t, err)
	require.Equal(t, 2, calls, "listPVC must follow the Continue token until it's empty")
	require.Len(t, entries, 3, "two PVCs plus the cluster itself")
}

func TestInventoryListPVCsError(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest()
	clientset := inv.clientset.(*k8sfake.Clientset)
	clientset.PrependReactor("list", "persistentvolumeclaims", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unreachable")
	})

	entries, err := runInventoryList(t, inv)
	require.ErrorContains(t, err, "apiserver unreachable")
	for _, e := range entries {
		require.NotEqual(t, pkg.ResourceSubClassPVC, e.SubClass,
			"no PVC entry when the listing failed")
	}
}

func TestInventoryClose(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest()
	require.NoError(t, inv.Close(t.Context()))
}

// clusterEntry is what listConfig emits for the cluster itself.
func clusterEntry() *sdk.InventoryEntry {
	return &sdk.InventoryEntry{
		URN:       "k8s:" + testClusterUID + ":cluster",
		Name:      "cluster",
		Endpoints: []sdk.HostEndpoint{{Type: sdk.EndpointIdentifier, Endpoint: "/"}},
	}
}

func TestInventoryListsTheCluster(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest()

	entries, err := runInventoryList(t, inv)
	require.NoError(t, err)
	require.Equal(t, []*sdk.InventoryEntry{clusterEntry()}, entries,
		"with no namespace given, the cluster itself is the only thing to back up")
}

func TestInventoryListsEachGivenNamespace(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest()
	inv.namespaces = []string{"ns1", "ns2"}

	entries, err := runInventoryList(t, inv)
	require.NoError(t, err)
	require.ElementsMatch(t, []*sdk.InventoryEntry{
		{
			URN:       "k8s:" + testClusterUID + ":ns1",
			Name:      "ns1",
			Endpoints: []sdk.HostEndpoint{{Type: sdk.EndpointIdentifier, Endpoint: "/ns1"}},
		},
		{
			URN:       "k8s:" + testClusterUID + ":ns2",
			Name:      "ns2",
			Endpoints: []sdk.HostEndpoint{{Type: sdk.EndpointIdentifier, Endpoint: "/ns2"}},
		},
	}, entries, "no cluster-wide entry when namespaces were given")
}

func TestInventoryUnidentifiableCluster(t *testing.T) {
	t.Parallel()

	inv := newInventoryTest()
	clientset := inv.clientset.(*k8sfake.Clientset)
	clientset.PrependReactor("get", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})

	_, err := runInventoryList(t, inv)
	require.ErrorContains(t, err, "failed to identify the cluster")
}
