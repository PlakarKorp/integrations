package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvcorev1 "kubevirt.io/api/core/v1"
	snapshotv1beta1 "kubevirt.io/api/snapshot/v1beta1"
	"sigs.k8s.io/yaml"
)

// vmConfigPath is where backupVm records the VM's own spec, captured from
// the VirtualMachineSnapshotContent.  pvcConfigPath is where each disk's
// original PVC spec is recorded, alongside its data under the same
// per-disk prefix.
const vmConfigPath = "/vm.yaml"

func pvcConfigPath(volumeName string) string {
	return path.Join("/", volumeName, "pvc.yaml")
}

// heuristic to stop waiting indefinitely if the guest agent never
// responds to the freeze request.
const vmSnapshotTimeout = 10 * time.Minute

func (k *k8s) getvm(ctx context.Context, ns, name string) (*kubevirtv1.VirtualMachine, error) {
	return k.kubevirtClient.KubevirtV1().VirtualMachines(ns).
		Get(ctx, name, metav1.GetOptions{})
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

// sanitizeMeta drops the metadata fields that describe a specific live
// instance of an object (resourceVersion, uid, creationTimestamp,
// managedFields, ...) rather than its desired state -- keeping those would
// make a captured manifest look like a specific past revision instead of a
// fresh object to (re)create, mirroring what apply.go strips before
// applying.
func sanitizeMeta(m metav1.ObjectMeta) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        m.Name,
		Namespace:   m.Namespace,
		Labels:      m.Labels,
		Annotations: m.Annotations,
	}
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
		ObjectMeta: sanitizeMeta(src.ObjectMeta),
		Spec:       src.Spec,
	}

	return yaml.Marshal(&vm)
}

// pvcConfig marshals a disk's original PVC (name + spec only) as a
// self-describing manifest, the same way vmConfig does for the VM itself.
// Captured at backup time from data already in hand (VolumeBackup embeds
// the full original PVC spec), so restoreVm never needs to re-derive a
// disk's PVC spec from the VM's volumes/DataVolumeTemplates.
func pvcConfig(pvc *corev1.PersistentVolumeClaim) ([]byte, error) {
	out := corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
		},
		ObjectMeta: sanitizeMeta(pvc.ObjectMeta),
		Spec:       pvc.Spec,
	}
	return yaml.Marshal(&out)
}

// degradedIndications are the SourceIndications meaning the snapshot is
// crash-consistent rather than application-consistent (no guest agent,
// quiesce timed out, or only some disks got included).  The non-deprecated
// SourceIndications field is used, not the older Indications one.
var degradedIndications = map[snapshotv1beta1.Indication]bool{
	snapshotv1beta1.VMSnapshotNoGuestAgentIndication:    true,
	snapshotv1beta1.VMSnapshotQuiesceTimeoutIndication:  true,
	snapshotv1beta1.VMSnapshotPartialSnapshotIndication: true,
}

// warnDegradedSnapshot logs, but does not fail, when a snapshot succeeded
// without being application-consistent -- the user should know, but a
// crash-consistent backup is still a usable one.
func warnDegradedSnapshot(vms *snapshotv1beta1.VirtualMachineSnapshot) {
	if vms.Status == nil {
		return
	}
	for _, si := range vms.Status.SourceIndications {
		if degradedIndications[si.Indication] {
			log.Printf("virtualmachinesnapshot %s/%s is not application-consistent: %s (%s)",
				vms.Namespace, vms.Name, si.Indication, si.Message)
		}
	}
}

// pushBytes emits a small in-memory manifest record (VM/PVC YAML) and waits
// for its ack before returning, matching the NEEDACK contract k8s+vm
// declares -- every record backupVm pushes must eventually be acked,
// whether it goes through podBackup/consume (which already does this) or,
// as here, straight from backupVm itself.
func pushBytes(records chan<- *connectors.Record, results <-chan *connectors.Result, pathname string, data []byte) error {
	rec := connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    int64(len(data)),
		Lmode:    0644,
		LmodTime: time.Now(),
	}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})

	records <- rec

	res, ok := <-results
	if !ok {
		return fmt.Errorf("results channel closed while waiting for an ack for %s", pathname)
	}
	return res.Err
}

// backupVm backs up a whole VM -- configuration and disks together -- as a
// single self-contained k8s+vm snapshot: /vm.yaml captures the VM spec,
// /<volumeName>/pvc.yaml captures each disk's original PVC spec, and
// /<volumeName>/... carries the disk's actual data, streamed the same way
// backupPvc does for a single PVC.
func (k *k8s) backupVm(ctx context.Context, ns, vmName string, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	vms, err := k.genvmsnap(ctx, ns, vmName)
	if err != nil {
		return fmt.Errorf("failed to snapshot vm %s/%s: %w", ns, vmName, err)
	}

	warnDegradedSnapshot(vms)

	contentName := *vms.Status.VirtualMachineSnapshotContentName
	content, err := k.kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshotContents(ns).
		Get(ctx, contentName, metav1.GetOptions{})
	if err != nil {
		k.delvmsnap(ctx, vms)
		return fmt.Errorf("failed to get virtualmachinesnapshotcontent %s/%s: %w", ns, contentName, err)
	}

	disks, err := k.diskSnapshots(ctx, vms)
	if err != nil {
		k.delvmsnap(ctx, vms)
		return fmt.Errorf("failed to resolve disk snapshots for vm %s/%s: %w", ns, vmName, err)
	}

	cfg, err := vmConfig(content)
	if err != nil {
		k.delvmsnap(ctx, vms)
		return fmt.Errorf("failed to extract vm config: %w", err)
	}

	if err := pushBytes(records, results, vmConfigPath, cfg); err != nil {
		k.delvmsnap(ctx, vms)
		return fmt.Errorf("failed to write %s: %w", vmConfigPath, err)
	}

	// clone every disk before releasing the VirtualMachineSnapshot:
	// deleting it earlier risks the controller garbage-collecting a
	// VolumeSnapshot while its clone is still being provisioned.
	clones := make([]*corev1.PersistentVolumeClaim, 0, len(disks))
	cleanupClones := func() {
		for _, pvc := range clones {
			k.delpvc(ctx, pvc)
		}
	}

	for _, disk := range disks {
		clone, err := k.pvcFromSnap(ctx, ns, disk.snapshot, disk.origPVC)
		if err != nil {
			cleanupClones()
			k.delvmsnap(ctx, vms)
			return fmt.Errorf("failed to clone disk %q: %w", disk.volumeName, err)
		}
		clones = append(clones, clone)
	}

	k.delvmsnap(ctx, vms)
	defer cleanupClones()

	for i, disk := range disks {
		pvcYAML, err := pvcConfig(disk.origPVC)
		if err != nil {
			return fmt.Errorf("failed to marshal pvc for disk %q: %w", disk.volumeName, err)
		}
		if err := pushBytes(records, results, pvcConfigPath(disk.volumeName), pvcYAML); err != nil {
			return fmt.Errorf("failed to write pvc manifest for disk %q: %w", disk.volumeName, err)
		}

		if err := k.backupDisk(ctx, ns, disk.volumeName, clones[i], records, results); err != nil {
			return fmt.Errorf("failed to back up disk %q: %w", disk.volumeName, err)
		}
	}

	return nil
}

// backupDisk streams one disk's data, the same fsServer/podBackup flow
// backupPvc uses for a single PVC, with its records re-pathed under
// /<volumeName>/... so multiple disks don't collide on the same path.
func (k *k8s) backupDisk(ctx context.Context, ns, volumeName string, pvc *corev1.PersistentVolumeClaim, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	fp, err := k.fsServer(ctx, "backup", ns, pvc, true)
	if err != nil {
		return fmt.Errorf("failed to create the pod: %w", err)
	}
	defer k.delpod(ctx, fp.pod)

	return k.podBackup(ctx, fp, "/"+volumeName, records, results)
}

// diskRestore accumulates one disk's captured PVC manifest and data records
// while restoreVm demultiplexes the single incoming record stream.
type diskRestore struct {
	pvcYAML []byte
	data    []*connectors.Record
}

// restoreVm restores a VM backed up by backupVm: recreates the VirtualMachine
// object from /vm.yaml, then for each disk recreates its PVC from
// /<volumeName>/pvc.yaml and streams the disk data into it.  Every incoming
// record is read fully in one pass first (cheap: connectors.Record's Reader
// is lazily opened, so buffering the *records* -- not their bytes -- costs
// nothing even for large disks) rather than assuming disks arrive in any
// particular order relative to each other.
func (k *k8s) restoreVm(ctx context.Context, ns, vmName string, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	var (
		vmYAML []byte
		disks  = map[string]*diskRestore{}
		order  []string
	)

	diskState := func(name string) *diskRestore {
		st, ok := disks[name]
		if !ok {
			st = &diskRestore{}
			disks[name] = st
			order = append(order, name)
		}
		return st
	}

	for record := range records {
		if record.Err != nil {
			results <- record.Ok()
			continue
		}

		if record.Pathname == vmConfigPath {
			data, err := io.ReadAll(record.Reader)
			if err != nil {
				res := record.Error(fmt.Errorf("failed to read %s: %w", vmConfigPath, err))
				results <- res
				return res.Err
			}
			vmYAML = data
			results <- record.Ok()
			continue
		}

		rel := strings.TrimPrefix(record.Pathname, "/")
		volumeName, sub, hasSub := strings.Cut(rel, "/")
		if volumeName == "" {
			res := record.Error(fmt.Errorf("unexpected record %q outside any disk", record.Pathname))
			results <- res
			return res.Err
		}

		st := diskState(volumeName)

		if hasSub && sub == "pvc.yaml" {
			data, err := io.ReadAll(record.Reader)
			if err != nil {
				res := record.Error(fmt.Errorf("failed to read pvc manifest for disk %q: %w", volumeName, err))
				results <- res
				return res.Err
			}
			st.pvcYAML = data
			results <- record.Ok()
			continue
		}

		newrecord := *record
		if hasSub {
			newrecord.Pathname = "/" + sub
		} else {
			newrecord.Pathname = "/"
		}
		st.data = append(st.data, &newrecord)
	}

	if vmYAML == nil {
		return fmt.Errorf("backup is missing %s", vmConfigPath)
	}

	var vm kvcorev1.VirtualMachine
	if err := yaml.Unmarshal(vmYAML, &vm); err != nil {
		return fmt.Errorf("failed to parse %s: %w", vmConfigPath, err)
	}
	vm.Namespace = ns
	if vm.Name == "" {
		vm.Name = vmName
	}

	if _, err := k.kubevirtClient.KubevirtV1().VirtualMachines(ns).
		Create(ctx, &vm, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create virtualmachine %s/%s: %w", ns, vm.Name, err)
	}

	for _, volumeName := range order {
		st := disks[volumeName]
		if st.pvcYAML == nil {
			return fmt.Errorf("disk %q is missing its pvc manifest", volumeName)
		}

		var pvc corev1.PersistentVolumeClaim
		if err := yaml.Unmarshal(st.pvcYAML, &pvc); err != nil {
			return fmt.Errorf("failed to parse pvc manifest for disk %q: %w", volumeName, err)
		}
		pvc.Namespace = ns

		created, err := k.clientset.CoreV1().PersistentVolumeClaims(ns).
			Create(ctx, &pvc, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create pvc for disk %q: %w", volumeName, err)
		}

		if err := k.restoreDisk(ctx, ns, created, st.data, results); err != nil {
			return fmt.Errorf("failed to restore disk %q: %w", volumeName, err)
		}
	}

	return nil
}

// restoreDisk streams one disk's already-demultiplexed data records back
// into its (freshly created) PVC, the same fsServer/podRestore flow
// restorePvc uses for a single PVC.
func (k *k8s) restoreDisk(ctx context.Context, ns string, pvc *corev1.PersistentVolumeClaim, data []*connectors.Record, results chan<- *connectors.Result) error {
	fp, err := k.fsServer(ctx, "restore", ns, pvc, false, "-export")
	if err != nil {
		return fmt.Errorf("failed to run the pod: %w", err)
	}
	defer k.delpod(ctx, fp.pod)

	ch := make(chan *connectors.Record, len(data))
	for _, r := range data {
		ch <- r
	}
	close(ch)

	return k.podRestore(ctx, fp, ch, results)
}
