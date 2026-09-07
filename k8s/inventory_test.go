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

func newInventoryTest(objs ...runtime.Object) *inventory {
	return &inventory{clientset: k8sfake.NewSimpleClientset(objs...)}
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
	inv := newInventoryTest(pvcObj("ns1", "pvc1"), pvcObj("ns2", "pvc2"))

	entries, err := runInventoryList(t, inv)
	require.NoError(t, err)
	require.ElementsMatch(t, []*sdk.InventoryEntry{
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
	require.Len(t, entries, 2)
}

func TestInventoryListPVCsError(t *testing.T) {
	inv := newInventoryTest()
	clientset := inv.clientset.(*k8sfake.Clientset)
	clientset.PrependReactor("list", "persistentvolumeclaims", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unreachable")
	})

	entries, err := runInventoryList(t, inv)
	require.ErrorContains(t, err, "apiserver unreachable")
	require.Empty(t, entries)
}

func TestInventoryClose(t *testing.T) {
	inv := newInventoryTest()
	require.NoError(t, inv.Close(t.Context()))
}
