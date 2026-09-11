package k8s

import (
	"errors"
	"log"
	"testing"

	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	snapfake "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned/fake"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	clienttesting "k8s.io/client-go/testing"
	kvcorev1 "kubevirt.io/api/core/v1"
	snapshotv1beta1 "kubevirt.io/api/snapshot/v1beta1"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
	"sigs.k8s.io/yaml"
)

func vmsnap(status *snapshotv1beta1.VirtualMachineSnapshotStatus) *snapshotv1beta1.VirtualMachineSnapshot {
	return &snapshotv1beta1.VirtualMachineSnapshot{Status: status}
}

func vmReadyToUse(x bool) *snapshotv1beta1.VirtualMachineSnapshotStatus {
	return &snapshotv1beta1.VirtualMachineSnapshotStatus{ReadyToUse: new(x)}
}

func vmFailed(msg string) *snapshotv1beta1.VirtualMachineSnapshotStatus {
	return &snapshotv1beta1.VirtualMachineSnapshotStatus{
		Error: &snapshotv1beta1.Error{Message: new(msg)},
	}
}

func namedVolumeSnapshot(ns, name string, status *vs.VolumeSnapshotStatus) *vs.VolumeSnapshot {
	return &vs.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     status,
	}
}

func TestVmsnapReady(t *testing.T) {
	suite := []struct {
		name    string
		evt     watch.Event
		want    bool
		wantErr string
	}{
		{
			name: "ready",
			evt:  watch.Event{Type: watch.Modified, Object: vmsnap(vmReadyToUse(true))},
			want: true,
		},
		{
			name: "not ready yet",
			evt:  watch.Event{Type: watch.Modified, Object: vmsnap(vmReadyToUse(false))},
			want: false,
		},
		{
			name:    "failed",
			evt:     watch.Event{Type: watch.Modified, Object: vmsnap(vmFailed("no guest agent"))},
			want:    false,
			wantErr: "no guest agent",
		},
		{
			name:    "error",
			evt:     watch.Event{Type: watch.Error, Object: &metav1.Status{Message: "invalid frobnication"}},
			want:    false,
			wantErr: "invalid frobnication",
		},
		{
			name: "unrelated object",
			evt:  watch.Event{Type: watch.Modified, Object: &corev1.Pod{}},
			want: false,
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			got, err := vmsnapReady(test.evt)

			if test.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.wantErr)
				require.False(t, got, "a failing virtualmachinesnapshot is never okay")
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func withGeneratedVmsnapResourceVersion(client *kubevirtfake.Clientset) {
	client.PrependReactor("create", "virtualmachinesnapshots", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(metav1.Object)
		if obj.GetName() == "" {
			obj.SetName(obj.GetGenerateName() + "generated")
		}
		obj.SetResourceVersion("1")
		return false, nil, nil
	})
}

func TestGenvmsnap(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		client := kubevirtfake.NewSimpleClientset()
		withGeneratedVmsnapResourceVersion(client)
		client.PrependWatchReactor("virtualmachinesnapshots", singleEventWatchReactor(watch.Event{
			Type: watch.Modified,
			Object: &snapshotv1beta1.VirtualMachineSnapshot{
				ObjectMeta: metav1.ObjectMeta{Name: "vmsnap-vm1-x", Namespace: "ns", ResourceVersion: "2"},
				Status:     vmReadyToUse(true),
			},
		}))

		k := &k8s{kubevirtClient: client}

		res, err := k.genvmsnap(t.Context(), "ns", "vm1")
		require.NoError(t, err)
		require.NotNil(t, res.Status)
		require.True(t, *res.Status.ReadyToUse)

		list, err := client.SnapshotV1beta1().VirtualMachineSnapshots("ns").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)
		require.Equal(t, "vm1", list.Items[0].Spec.Source.Name)
		require.Equal(t, "VirtualMachine", list.Items[0].Spec.Source.Kind)
		require.NotNil(t, list.Items[0].Spec.Source.APIGroup)
		require.Equal(t, "kubevirt.io", *list.Items[0].Spec.Source.APIGroup)
	})

	t.Run("failed status deletes the snapshot and returns the error", func(t *testing.T) {
		client := kubevirtfake.NewSimpleClientset()
		withGeneratedVmsnapResourceVersion(client)
		client.PrependWatchReactor("virtualmachinesnapshots", singleEventWatchReactor(watch.Event{
			Type: watch.Modified,
			Object: &snapshotv1beta1.VirtualMachineSnapshot{
				ObjectMeta: metav1.ObjectMeta{Name: "vmsnap-vm1-x", Namespace: "ns", ResourceVersion: "2"},
				Status:     vmFailed("no guest agent"),
			},
		}))

		var deleted bool
		client.PrependReactor("delete", "virtualmachinesnapshots", func(clienttesting.Action) (bool, runtime.Object, error) {
			deleted = true
			return false, nil, nil
		})

		k := &k8s{kubevirtClient: client}

		_, err := k.genvmsnap(t.Context(), "ns", "vm1")
		require.ErrorContains(t, err, "no guest agent")
		require.True(t, deleted, "genvmsnap must delete the virtualmachinesnapshot when it failed")
	})


	// don't attempt to test watch.Error because watchtools.Until uses a
	// RetryWatcher that would end up retrying forever.
}

func TestDelvmsnap(t *testing.T) {
	newVms := func() *snapshotv1beta1.VirtualMachineSnapshot {
		return &snapshotv1beta1.VirtualMachineSnapshot{ObjectMeta: metav1.ObjectMeta{Name: "vmsnap1", Namespace: "ns"}}
	}

	t.Run("deletes the virtualmachinesnapshot", func(t *testing.T) {
		vms := newVms()
		client := kubevirtfake.NewSimpleClientset(vms)
		k := &k8s{kubevirtClient: client}

		k.delvmsnap(t.Context(), vms)

		list, err := client.SnapshotV1beta1().VirtualMachineSnapshots("ns").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Empty(t, list.Items)
	})

	t.Run("delete error is only logged", func(t *testing.T) {
		vms := newVms()
		client := kubevirtfake.NewSimpleClientset(vms)
		client.PrependReactor("delete", "virtualmachinesnapshots", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
		k := &k8s{kubevirtClient: client}

		require.NotPanics(t, func() { k.delvmsnap(t.Context(), vms) })
	})
}

func TestDiskSnapshots(t *testing.T) {
	newContent := func(vbs ...snapshotv1beta1.VolumeBackup) *snapshotv1beta1.VirtualMachineSnapshotContent {
		return &snapshotv1beta1.VirtualMachineSnapshotContent{
			ObjectMeta: metav1.ObjectMeta{Name: "content1", Namespace: "ns"},
			Spec:       snapshotv1beta1.VirtualMachineSnapshotContentSpec{VolumeBackups: vbs},
		}
	}

	vmsFor := func(contentName string) *snapshotv1beta1.VirtualMachineSnapshot {
		return &snapshotv1beta1.VirtualMachineSnapshot{
			ObjectMeta: metav1.ObjectMeta{Name: "vmsnap1", Namespace: "ns"},
			Status: &snapshotv1beta1.VirtualMachineSnapshotStatus{
				VirtualMachineSnapshotContentName: &contentName,
			},
		}
	}

	t.Run("correlates each disk to its volumesnapshot and original pvc", func(t *testing.T) {
		vb1 := snapshotv1beta1.VolumeBackup{
			VolumeName: "disk0",
			PersistentVolumeClaim: snapshotv1beta1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "disk0-pvc"},
				Spec: corev1.PersistentVolumeClaimSpec{
					VolumeMode: new(corev1.PersistentVolumeBlock),
				},
			},
			VolumeSnapshotName: new("snap-disk0"),
		}
		vb2 := snapshotv1beta1.VolumeBackup{
			VolumeName: "disk1",
			PersistentVolumeClaim: snapshotv1beta1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "disk1-pvc"},
			},
			VolumeSnapshotName: new("snap-disk1"),
		}
		content := newContent(vb1, vb2)

		kvClient := kubevirtfake.NewSimpleClientset(content)
		snapClient := snapfake.NewSimpleClientset(
			namedVolumeSnapshot("ns", "snap-disk0", readyToUse(true)),
			namedVolumeSnapshot("ns", "snap-disk1", readyToUse(true)),
		)

		k := &k8s{kubevirtClient: kvClient, snapClient: snapClient}

		disks, err := k.gendisksnap(t.Context(), vmsFor("content1"), content)
		require.NoError(t, err)
		require.Len(t, disks, 2)

		require.Equal(t, "disk0", disks[0].volumeName)
		require.Equal(t, "snap-disk0", disks[0].snapshot.Name)
		require.Equal(t, "disk0-pvc", disks[0].origPVC.Name)
		require.Equal(t, "ns", disks[0].origPVC.Namespace)
		require.NotNil(t, disks[0].origPVC.Spec.VolumeMode)
		require.Equal(t, corev1.PersistentVolumeBlock, *disks[0].origPVC.Spec.VolumeMode)

		require.Equal(t, "disk1", disks[1].volumeName)
		require.Equal(t, "snap-disk1", disks[1].snapshot.Name)
		require.Equal(t, "disk1-pvc", disks[1].origPVC.Name)
	})

	t.Run("nil VolumeSnapshotName is a partial-snapshot error, not a panic", func(t *testing.T) {
		vb := snapshotv1beta1.VolumeBackup{
			VolumeName: "disk0",
			PersistentVolumeClaim: snapshotv1beta1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "disk0-pvc"},
			},
		}
		content := newContent(vb)
		k := &k8s{
			kubevirtClient: kubevirtfake.NewSimpleClientset(content),
			snapClient:     snapfake.NewSimpleClientset(),
		}

		var (
			disks []diskBackup
			err   error
		)
		require.NotPanics(t, func() {
			disks, err = k.gendisksnap(t.Context(), vmsFor("content1"), content)
		})
		require.ErrorContains(t, err, `disk "disk0" has no volumesnapshot`)
		require.Nil(t, disks)
	})

	t.Run("missing volumesnapshot propagates the api error", func(t *testing.T) {
		vb := snapshotv1beta1.VolumeBackup{
			VolumeName: "disk0",
			PersistentVolumeClaim: snapshotv1beta1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "disk0-pvc"},
			},
			VolumeSnapshotName: new("snap-disk0"),
		}
		content := newContent(vb)
		k := &k8s{
			kubevirtClient: kubevirtfake.NewSimpleClientset(content),
			snapClient:     snapfake.NewSimpleClientset(), // snap-disk0 was never created
		}

		_, err := k.gendisksnap(t.Context(), vmsFor("content1"), content)
		require.ErrorContains(t, err, "snap-disk0")
	})
}

func TestVmConfig(t *testing.T) {
	t.Run("marshals the embedded VirtualMachine as a self-describing manifest", func(t *testing.T) {
		src := &snapshotv1beta1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "vm1", Namespace: "ns"},
			Spec:       kvcorev1.VirtualMachineSpec{Running: new(true)},
		}
		content := &snapshotv1beta1.VirtualMachineSnapshotContent{
			ObjectMeta: metav1.ObjectMeta{Name: "content1", Namespace: "ns"},
			Spec: snapshotv1beta1.VirtualMachineSnapshotContentSpec{
				Source: snapshotv1beta1.SourceSpec{VirtualMachine: src},
			},
		}

		out, err := vmconfig(content)
		require.NoError(t, err)

		log.Println("out is", string(out))

		var got kvcorev1.VirtualMachine
		require.NoError(t, yaml.Unmarshal(out, &got))

		require.Equal(t, "kubevirt.io/v1", got.APIVersion)
		require.Equal(t, "VirtualMachine", got.Kind)
		require.Equal(t, "vm1", got.Name)
		require.Equal(t, "ns", got.Namespace)
		require.NotNil(t, got.Spec.Running)
		require.True(t, *got.Spec.Running)
	})

	t.Run("errors when the content has no source VirtualMachine", func(t *testing.T) {
		content := &snapshotv1beta1.VirtualMachineSnapshotContent{
			ObjectMeta: metav1.ObjectMeta{Name: "content1", Namespace: "ns"},
		}

		_, err := vmconfig(content)
		require.ErrorContains(t, err, "has no source VirtualMachine")
	})
}
