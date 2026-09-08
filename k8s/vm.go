package k8s

import (
	"context"
	"fmt"
	"log"
	"time"

	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	kvcorev1 "kubevirt.io/api/core/v1"
	snapshotv1beta1 "kubevirt.io/api/snapshot/v1beta1"
	"sigs.k8s.io/yaml"
)

// heuristic to stop waiting indefinitely if the guest agent never
// responds to the freeze request.
const vmSnapshotTimeout = 10 * time.Minute

func vmsnapReady(evt watch.Event) (bool, error) {
	if evt.Type == watch.Error {
		return false, apierrors.FromObject(evt.Object)
	}

	vms, ok := evt.Object.(*snapshotv1beta1.VirtualMachineSnapshot)
	if !ok {
		return false, nil
	}

	if vms.Status != nil && vms.Status.Error != nil && vms.Status.Error.Message != nil {
		return false, fmt.Errorf("%s", *vms.Status.Error.Message)
	}

	return vms.Status != nil && vms.Status.ReadyToUse != nil && *vms.Status.ReadyToUse, nil
}

func (k *k8s) genvmsnap(ctx context.Context, ns, vmName string) (*snapshotv1beta1.VirtualMachineSnapshot, error) {
	apiGroup := "kubevirt.io"
	vms := &snapshotv1beta1.VirtualMachineSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "vmsnap-" + vmName + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
			},
		},
		Spec: snapshotv1beta1.VirtualMachineSnapshotSpec{
			Source: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "VirtualMachine",
				Name:     vmName,
			},
		},
	}

	vms, err := k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshots(ns).
		Create(ctx, vms, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	ready, err := k.waitvmsnap(ctx, vms)
	if err != nil {
		k.delvmsnap(ctx, vms)
		return nil, err
	}
	return ready, nil
}

func (k *k8s) waitvmsnap(ctx context.Context, vms *snapshotv1beta1.VirtualMachineSnapshot) (*snapshotv1beta1.VirtualMachineSnapshot, error) {
	wctx, cancel := context.WithTimeout(ctx, vmSnapshotTimeout)
	defer cancel()

	lw := &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = "metadata.name=" + vms.Name
			return k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshots(vms.Namespace).Watch(ctx, opts)
		},
	}

	evt, err := watchtools.Until(wctx, vms.ResourceVersion, lw, vmsnapReady)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if wctx.Err() != nil {
			return nil, fmt.Errorf("virtualmachinesnapshot %s/%s did not become ready within %s",
				vms.Namespace, vms.Name, vmSnapshotTimeout)
		}
		return nil, err
	}

	ready, ok := evt.Object.(*snapshotv1beta1.VirtualMachineSnapshot)
	if !ok {
		return nil, fmt.Errorf("unexpected object %T from the virtualmachinesnapshot watch", evt.Object)
	}
	return ready, nil
}

func (k *k8s) delvmsnap(ctx context.Context, vms *snapshotv1beta1.VirtualMachineSnapshot) {
	ctx, cancel := detached(ctx)
	defer cancel()

	err := k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshots(vms.Namespace).
		Delete(ctx, vms.Name, metav1.DeleteOptions{})
	if err != nil {
		log.Printf("failed to delete virtualmachinesnapshot %s/%s: %s",
			vms.Namespace, vms.Name, err)
	}
}

type diskBackup struct {
	volumeName string
	snapshot   *vs.VolumeSnapshot
	origPVC    *corev1.PersistentVolumeClaim
}

func (k *k8s) diskSnapshots(ctx context.Context, vms *snapshotv1beta1.VirtualMachineSnapshot) ([]diskBackup, error) {
	if vms.Status == nil || vms.Status.VirtualMachineSnapshotContentName == nil {
		return nil, fmt.Errorf("virtualmachinesnapshot %s/%s has no content",
			vms.Namespace, vms.Name)
	}

	contentName := *vms.Status.VirtualMachineSnapshotContentName
	content, err := k.kubevirtClient.SnapshotV1beta1().
		VirtualMachineSnapshotContents(vms.Namespace).
		Get(ctx, contentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get virtualmachinesnapshotcontent %s/%s: %w",
			vms.Namespace, contentName, err)
	}

	disks := make([]diskBackup, 0, len(content.Spec.VolumeBackups))
	for _, vb := range content.Spec.VolumeBackups {
		if vb.VolumeSnapshotName == nil {
			return nil, fmt.Errorf("disk %q has no volumesnapshot (partial virtualmachinesnapshot?)",
				vb.VolumeName)
		}

		snap, err := k.getsnap(ctx, vms.Namespace, *vb.VolumeSnapshotName)
		if err != nil {
			return nil, fmt.Errorf("failed to adopt volumesnapshot %s/%s for disk %q: %w",
				vms.Namespace, *vb.VolumeSnapshotName, vb.VolumeName, err)
		}

		orig := &corev1.PersistentVolumeClaim{
			ObjectMeta: vb.PersistentVolumeClaim.ObjectMeta,
			Spec:       vb.PersistentVolumeClaim.Spec,
		}
		orig.Namespace = vms.Namespace

		disks = append(disks, diskBackup{
			volumeName: vb.VolumeName,
			snapshot:   snap,
			origPVC:    orig,
		})
	}

	return disks, nil
}

func vmConfig(content *snapshotv1beta1.VirtualMachineSnapshotContent) ([]byte, error) {
	src := content.Spec.Source.VirtualMachine
	if src == nil {
		return nil, fmt.Errorf("virtualmachinesnapshotcontent %s/%s has no source VirtualMachine",
			content.Namespace, content.Name)
	}

	vm := kvcorev1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kvcorev1.GroupVersion.String(),
			Kind:       "VirtualMachine",
		},
		ObjectMeta: src.ObjectMeta,
		Spec:       src.Spec,
	}

	return yaml.Marshal(&vm)
}
