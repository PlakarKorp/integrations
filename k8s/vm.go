package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/integrations/k8s/tee"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/objects"
	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvcorev1 "kubevirt.io/api/core/v1"
	snapshotv1beta1 "kubevirt.io/api/snapshot/v1beta1"
	"sigs.k8s.io/yaml"
)

const (
	// heuristic to stop waiting indefinitely if the guest agent
	// never responds to the freeze request.
	vmSnapshotTimeout = 10 * time.Minute
)

func (k *k8s) getvm(ctx context.Context, ns, name string) (*kubevirtv1.VirtualMachine, error) {
	return k.kubevirtClient.KubevirtV1().VirtualMachines(ns).
		Get(ctx, name, metav1.GetOptions{})
}

func (k *k8s) delvmsnap(ctx context.Context, vms *snapshotv1beta1.VirtualMachineSnapshot) {
	ctx, cancel := detached(ctx)
	defer cancel()

	err := k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshots(vms.Namespace).
		Delete(ctx, vms.Name, metav1.DeleteOptions{})
	if err != nil {
		log.Printf("failed to delete VirtualMachineSnapshot %s/%s: %s",
			vms.Namespace, vms.Name, err)
	}
}

func vmsnapReady(evt watch.Event) (bool, error) {
	if evt.Type == watch.Error {
		return false, apierrors.FromObject(evt.Object)
	}

	vms, ok := evt.Object.(*snapshotv1beta1.VirtualMachineSnapshot)
	if !ok {
		return false, nil
	}

	if vms.Status != nil && vms.Status.Error != nil && vms.Status.Error.Message != nil {
		return false, errors.New(*vms.Status.Error.Message)
	}

	return vms.Status != nil && vms.Status.ReadyToUse != nil && *vms.Status.ReadyToUse, nil
}

func (k *k8s) waitvmsnap(ctx context.Context, vms *snapshotv1beta1.VirtualMachineSnapshot) (*snapshotv1beta1.VirtualMachineSnapshot, error) {
	wctx, cancel := context.WithTimeout(ctx, vmSnapshotTimeout)
	defer cancel()

	lw := &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = "metadata.name=" + vms.Name
			return k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshots(vms.Namespace).
				Watch(ctx, opts)
		},
	}

	evt, err := watchtools.Until(wctx, vms.ResourceVersion, lw, vmsnapReady)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if wctx.Err() != nil {
			return nil, fmt.Errorf("VirtualMachineSnapshot %s/%s did not come ready within %s",
				vms.Namespace, vms.Name, vmSnapshotTimeout)
		}
		return nil, err
	}

	ready, ok := evt.Object.(*snapshotv1beta1.VirtualMachineSnapshot)
	if !ok {
		return nil, fmt.Errorf("unexpected object %T from the VirtualMachineSnapshot watcher",
			evt.Object)
	}
	return ready, nil
}

func (k *k8s) genvmsnap(ctx context.Context, ns, name string) (*snapshotv1beta1.VirtualMachineSnapshot, error) {
	vms := &snapshotv1beta1.VirtualMachineSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "vmsnap-" + name + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
			},
		},
		Spec: snapshotv1beta1.VirtualMachineSnapshotSpec{
			Source: corev1.TypedLocalObjectReference{
				APIGroup: new("kubevirt.io"),
				Kind:     "VirtualMachine",
				Name:     name,
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

func consistentvm(vms *snapshotv1beta1.VirtualMachineSnapshot) error {
	if vms.Status == nil {
		return nil
	}

	indications := map[snapshotv1beta1.Indication]bool{
		snapshotv1beta1.VMSnapshotNoGuestAgentIndication:    true,
		snapshotv1beta1.VMSnapshotQuiesceTimeoutIndication:  true,
		snapshotv1beta1.VMSnapshotPartialSnapshotIndication: true,
	}

	for _, si := range vms.Status.SourceIndications {
		if indications[si.Indication] {
			return fmt.Errorf("%s (%s)", si.Indication, si.Message)
		}
	}
	return nil
}

type diskBackup struct {
	volumeName string
	snapshot   *vs.VolumeSnapshot
	origPVC    *corev1.PersistentVolumeClaim
}

func (k *k8s) gendisksnap(ctx context.Context, vms *snapshotv1beta1.VirtualMachineSnapshot, content *snapshotv1beta1.VirtualMachineSnapshotContent) ([]diskBackup, error) {
	if vms.Status == nil || vms.Status.VirtualMachineSnapshotContentName == nil {
		return nil, fmt.Errorf("virtualmachinesnapshot %s/%s has no content",
			vms.Namespace, vms.Name)
	}

	disks := make([]diskBackup, 0, len(content.Spec.VolumeBackups))
	for _, vb := range content.Spec.VolumeBackups {
		if vb.VolumeSnapshotName == nil {
			return nil, fmt.Errorf("disk %q has no volumesnapshot (partial VirtualMachineSnapshot?)",
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

func vmconfig(content *snapshotv1beta1.VirtualMachineSnapshotContent) ([]byte, error) {
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

func manifest(records chan<- *connectors.Record, results <-chan *connectors.Result, pathname string, content []byte) error {
	records <- connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    int64(len(content)),
		Lmode:    0644,
		LmodTime: time.Now(),
	}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})

	res, ok := <-results
	if !ok {
		return fmt.Errorf("results channel closed while waiting for an ack for %s", pathname)
	}
	return res.Err
}

func (k *k8s) backupDisk(ctx context.Context, ns, prefix string, pvc *corev1.PersistentVolumeClaim, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	fp, err := k.fsServer(ctx, "backup", ns, pvc, true)
	if err != nil {
		return fmt.Errorf("failed to create the pod: %w", err)
	}
	defer k.delpod(ctx, fp.pod)

	return k.podBackup(ctx, fp, prefix, records, results)
}

func (k *k8s) backupVM(ctx context.Context, ns, name string, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	vms, err := k.genvmsnap(ctx, ns, name)
	if err != nil {
		return fmt.Errorf("failed to snapshot vm %s/%s: %w", ns, name, err)
	}
	defer k.delvmsnap(ctx, vms)

	if err := consistentvm(vms); err != nil {
		log.Printf("VirtualMachineSnapshot %s/%s is not application-consistent: %s",
			ns, name, err)
	}

	contentName := *vms.Status.VirtualMachineSnapshotContentName
	content, err := k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshotContents(ns).
		Get(ctx, contentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get virtualmachinesnapshotcontent %s/%s: %w", ns, contentName, err)
	}

	disks, err := k.gendisksnap(ctx, vms, content)
	if err != nil {
		return fmt.Errorf("failed to resolve disk snapshots for vm %s/%s: %w", ns, name, err)
	}

	cfg, err := vmconfig(content)
	if err != nil {
		return fmt.Errorf("failed to extract vm config: %w", err)
	}

	if err := manifest(records, results, "/vm.yaml", cfg); err != nil {
		return fmt.Errorf("failed to write /vm.yaml: %w", err)
	}

	for _, disk := range disks {
		pvc, err := k.pvcFromSnap(ctx, ns, disk.snapshot, disk.origPVC)
		if err != nil {
			return fmt.Errorf("failed to clone disk %q: %w", disk.volumeName, err)
		}

		content, err := yaml.Marshal(pvc)
		if err != nil {
			k.delpvc(ctx, pvc)
			return fmt.Errorf("failed to marshal disk %q config: %w",
				disk.volumeName, err)
		}

		pathname := path.Join("/", disk.volumeName, "config.yaml")
		if err := manifest(records, results, pathname, content); err != nil {
			k.delpvc(ctx, pvc)
			return fmt.Errorf("failed to write %s: %w", pathname, err)
		}

		prefix := path.Join("/", disk.volumeName, "data")
		if err := k.backupDisk(ctx, ns, prefix, pvc, records, results); err != nil {
			k.delpvc(ctx, pvc)
			return fmt.Errorf("failed to back up disk %q: %w", disk.volumeName, err)
		}

		k.delpvc(ctx, pvc)
	}

	return nil
}

// restorablepvc returns a copy of a backed up PVC that can be created
// again, keeping only the fields that describe what to provision.  The
// PV itself is never restored: it's cluster-scoped and its volumeHandle
// names a volume in the storage backend that doesn't exist here, and
// the data is streamed back in anyway.  So the claim has to go through
// dynamic provisioning again, which means dropping:
//
//   - volumeName, which pins the claim to a PV that isn't here and
//     skips dynamic provisioning altogether, leaving it Pending;
//   - dataSource and dataSourceRef, which point at the VolumeSnapshot
//     the backup was taken from, long gone by now;
//   - the bind annotations, since bind-completed without a volumeName
//     makes the PV controller treat the claim as bound to a volume
//     that vanished and park it in Lost.
func restorablepvc(pvc *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
	var annotations map[string]string
	for key, value := range pvc.Annotations {
		if strings.HasPrefix(key, "pv.kubernetes.io/") ||
			strings.HasPrefix(key, "volume.kubernetes.io/") ||
			strings.HasPrefix(key, "volume.beta.kubernetes.io/") {
			continue
		}
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[key] = value
	}

	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        pvc.Name,
			Namespace:   pvc.Namespace, // XXX should be overwriteable
			Labels:      pvc.Labels,
			Annotations: annotations,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      pvc.Spec.AccessModes,
			Resources:        pvc.Spec.Resources,
			StorageClassName: pvc.Spec.StorageClassName,
			VolumeMode:       pvc.Spec.VolumeMode,
		},
	}
}

func (k *k8s) pvcfrom(u *unstructured.Unstructured) (*corev1.PersistentVolumeClaim, error) {
	var pvcGVK = corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim")
	if gvk := u.GroupVersionKind(); gvk != pvcGVK {
		return nil, fmt.Errorf("expected %s but got %s", pvcGVK, gvk)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &pvc); err != nil {
		return nil, fmt.Errorf("failed to convert %s into a PersistentVolumeClaim: %w",
			u.GetName(), err)
	}

	return restorablepvc(&pvc), nil
}

func (k *k8s) restoreVM(ctx context.Context, ns, name string, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	var (
		vmconf     []byte
		currentexp *tee.Exporter
	)

	for record := range records {
		if record.Pathname == "/vm.yaml" {
			if vmconf != nil {
				err := errors.New("/vm.yaml already seen")
				results <- record.Error(err)
				return err
			}

			b, err := io.ReadAll(record.Reader)
			if err != nil {
				err = fmt.Errorf("failed to read %q: %w", record.Pathname, err)
				results <- record.Error(err)
				return err
			}

			vmconf = b
			results <- record.Ok()
			continue
		}

		pvc, rest, ok := strings.Cut(strings.TrimPrefix(record.Pathname, "/"), "/")
		if ok {
			err := fmt.Errorf("unexpected record %q", record.Pathname)
			results <- record.Error(err)
			return err
		}

		if rest == "config.yaml" {
			// new pvc.
			if currentexp != nil {
				if err := currentexp.Wait(); err != nil {
					results <- record.Ok() // small lie
					return err
				}
				currentexp = nil
			}
		}
	}

	_, err := k.apply(ctx, connectors.NewRecord("/vm.yaml", "", objects.FileInfo{
		Lname: "vm.yaml",
		Lsize: int64(len(vmconf)),
	}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(vmconf)), nil
	}))
	return err
}
