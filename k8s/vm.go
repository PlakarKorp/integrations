package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"runtime"
	"strings"
	"time"

	gexporter "github.com/PlakarKorp/integration-grpc/exporter"
	"github.com/PlakarKorp/integrations/k8s/mtls"
	"github.com/PlakarKorp/integrations/k8s/tee"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/objects"
	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
		Status:     src.Status,
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

		// manually fill the TypeMeta since it's left empty by
		// client-go.  Also, we need to recreate the original
		// claim, not the clone we're backing up.
		orig := *disk.origPVC
		orig.TypeMeta = metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
		}

		content, err := yaml.Marshal(&orig)
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

// restorablepvc transforms the given pvc into something that we can
// reapply: we don't restore the original PV.
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

// restorablevm turns the snapshot of the vm descriptor we got into
// something that we can apply again.
func restorablevm(vm *kvcorev1.VirtualMachine) *kvcorev1.VirtualMachine {
	var dropAnnotations = map[string]bool{
		"kubectl.kubernetes.io/last-applied-configuration": true,
		"kubevirt.io/latest-observed-api-version":          true,
		"kubevirt.io/storage-observed-api-version":         true,
	}

	var annotations map[string]string
	for key, value := range vm.Annotations {
		if dropAnnotations[key] {
			continue
		}
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[key] = value
	}

	return &kvcorev1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kvcorev1.GroupVersion.String(),
			Kind:       "VirtualMachine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        vm.Name,
			Namespace:   vm.Namespace,
			Labels:      vm.Labels,
			Annotations: annotations,
		},
		Spec: vm.Spec,
	}
}

func vmfrom(content []byte) (*kvcorev1.VirtualMachine, error) {
	var vm kvcorev1.VirtualMachine
	if err := yaml.Unmarshal(content, &vm); err != nil {
		return nil, fmt.Errorf("failed to parse the VirtualMachine: %w", err)
	}

	// ensure that what we parsed was really a VM
	vmGVK := kvcorev1.GroupVersion.WithKind("VirtualMachine")
	if gvk := vm.GroupVersionKind(); gvk != vmGVK {
		return nil, fmt.Errorf("expected %s but got %s", vmGVK, gvk)
	}

	return restorablevm(&vm), nil
}

func pvcfrom(content []byte) (*corev1.PersistentVolumeClaim, error) {
	var pvc corev1.PersistentVolumeClaim
	if err := yaml.Unmarshal(content, &pvc); err != nil {
		return nil, fmt.Errorf("failed to parse the PersistentVolumeClaim: %w", err)
	}

	// ensure that what we parsed was really a PVC
	pvcGVK := corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim")
	if gvk := pvc.GroupVersionKind(); gvk != pvcGVK {
		return nil, fmt.Errorf("expected %s but got %s", pvcGVK, gvk)
	}

	return restorablepvc(&pvc), nil
}

type smolexporter struct {
	exporter.Exporter
	client  *grpc.ClientConn
	stop    chan struct{}
	killpod func(ctx context.Context)
}

func (s *smolexporter) Close(ctx context.Context) error {
	err := errors.Join(s.Exporter.Close(ctx), s.client.Close())
	s.killpod(ctx)
	if s.stop != nil {
		close(s.stop)
	}
	return err
}

func (k *k8s) exporterfor(ctx context.Context, pvc *corev1.PersistentVolumeClaim) (exporter.Exporter, error) {
	fp, err := k.fsServer(ctx, "restore", pvc.Namespace, pvc, false, "-export")
	if err != nil {
		return nil, fmt.Errorf("failed to run the pod: %w", err)
	}

	url, stop, err := k.urlFor(ctx, fp.pod)
	if err != nil {
		k.delpod(ctx, fp.pod)
		return nil, err
	}

	cred := credentials.NewTLS(mtls.ClientTlsConfig(fp.cert, fp.peer))
	client, err := grpc.NewClient(url, grpc.WithTransportCredentials(cred))
	if err != nil {
		k.delpod(ctx, fp.pod)
		if stop != nil {
			close(stop)
		}
		return nil, fmt.Errorf("failed to create a grpc client for %s: %w", url, err)
	}

	proto, path := "fs", fsPath
	if fp.block {
		proto, path = "block", blockPath
	}

	opts := &connectors.Options{
		Hostname:        "plakar-pod",
		OperatingSystem: "linux",
		Architecture:    runtime.GOARCH,
		CWD:             path,
		MaxConcurrency:  k.opts.MaxConcurrency,
	}

	exporter, err := gexporter.NewExporter(ctx, client, opts, proto, map[string]string{
		"location": proto + "://" + path,
	})
	if err != nil {
		k.delpod(ctx, fp.pod)
		if stop != nil {
			close(stop)
		}
		return nil, fmt.Errorf("failed to run the grpc exporter: %w", err)
	}

	return &smolexporter{
		Exporter: exporter,
		client:   client,
		stop:     stop,
		killpod: func(ctx context.Context) {
			k.delpod(ctx, fp.pod)
		},
	}, nil
}

func (k *k8s) restoreVM(ctx context.Context, ns, name string, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	var (
		vmconf     []byte
		currentexp *tee.Exporter
		currentvol string
		prefix     string
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

		vol, rest, ok := strings.Cut(strings.TrimPrefix(record.Pathname, "/"), "/")
		if !ok {
			results <- record.Ok()
			continue
		}

		if vol != currentvol && rest != "config.yaml" {
			err := fmt.Errorf("unexpected record: was in volume %q now we're in %q",
				currentvol, vol)
			results <- record.Error(err)
			return err
		}

		if rest == "config.yaml" {
			// new pvc.
			if currentexp != nil {
				if err := currentexp.Wait(); err != nil {
					// small lie: this record is not even
					// considered, yet we're acknowleging
					// for good measure, while returning an
					// hard failure.
					results <- record.Ok()
					return err
				}
				currentexp = nil
			}

			b, err := io.ReadAll(record.Reader)
			if err != nil {
				err = fmt.Errorf("failed to read %q: %w", record.Pathname, err)
				results <- record.Error(err)
				return err
			}

			pvc, err := pvcfrom(b)
			if err != nil {
				err = fmt.Errorf("failed to parse %q as pvc: %w", record.Pathname, err)
				results <- record.Error(err)
				return err
			}

			b, err = yaml.Marshal(pvc)
			if err != nil {
				err = fmt.Errorf("failed to marshal %q as pvc: %w", record.Pathname, err)
				results <- record.Error(err)
				return err
			}

			if err := k.apply(ctx, record.Pathname, bytes.NewReader(b)); err != nil {
				err = fmt.Errorf("failed to apply pvc %q: %w", record.Pathname, err)
				results <- record.Error(err)
				return err
			}

			exp, err := k.exporterfor(ctx, pvc)
			if err != nil {
				err = fmt.Errorf("failed to apply pvc %q: %w", record.Pathname, err)
				results <- record.Error(err)
				return err
			}

			currentexp = tee.New(ctx, exp)
			currentvol = vol
			prefix = path.Join("/", vol, "data")
			results <- record.Ok()
			continue
		}

		if rest == "data" {
			results <- record.Ok()
			continue
		}

		if !strings.HasPrefix(rest, "data/") {
			err := errors.New("unexpected file found")
			results <- record.Error(err)
			return err
		}

		if currentexp == nil {
			err := fmt.Errorf("missing PVC config.yaml before data for volume %s", vol)
			results <- record.Error(err)
			return err
		}

		pathname := strings.TrimPrefix(record.Pathname, prefix)
		newrecord := *record
		newrecord.Pathname = pathname
		newrecord.FileInfo.Lname = path.Base(pathname)

		res, err := currentexp.Push(&newrecord)
		if err != nil {
			results <- record.Error(err)
			return err
		}

		results <- &connectors.Result{Record: *record, Err: res.Err}
		if res.Err != nil {
			return res.Err
		}
	}

	if currentexp != nil {
		if err := currentexp.Wait(); err != nil {
			return fmt.Errorf("failed to close exporter for pvc %s: %w", currentvol, err)
		}
	}

	if vmconf == nil {
		return errors.New("never seen /vm.yaml, not restoring a vm")
	}

	vm, err := vmfrom(vmconf)
	if err != nil {
		return fmt.Errorf("failed to parse /vm.yaml: %w", err)
	}

	conf, err := yaml.Marshal(vm)
	if err != nil {
		return fmt.Errorf("failed to marshal the VirtualMachine: %w", err)
	}

	return k.apply(ctx, "/vm.yaml", bytes.NewReader(conf))
}
