package k8s

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	gexporter "github.com/PlakarKorp/integration-grpc/exporter"
	gimporter "github.com/PlakarKorp/integration-grpc/importer"
	"github.com/PlakarKorp/integrations/k8s/mtls"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/google/uuid"
	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/portforward"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/client-go/transport/spdy"
)

const kubeletContainer = "kubelet"

var fatalWaiting = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

func snapshotReady(evt watch.Event) (bool, error) {
	if evt.Type == watch.Error {
		return false, apierrors.FromObject(evt.Object)
	}

	s, ok := evt.Object.(*vs.VolumeSnapshot)
	if !ok {
		return false, nil
	}

	if s.Status != nil && s.Status.Error != nil && s.Status.Error.Message != nil {
		return false, fmt.Errorf("%s", *s.Status.Error.Message)
	}

	return s.Status != nil && s.Status.ReadyToUse != nil && *s.Status.ReadyToUse, nil
}

func podReady(evt watch.Event) (bool, error) {
	if evt.Type == watch.Error {
		return false, apierrors.FromObject(evt.Object)
	}

	p, ok := evt.Object.(*corev1.Pod)
	if !ok {
		return false, nil
	}

	if evt.Type == watch.Deleted {
		return false, fmt.Errorf("pod %s/%s was deleted while starting",
			p.Namespace, p.Name)
	}

	if p.Status.Phase == corev1.PodFailed {
		return false, fmt.Errorf("pod %s/%s failed: %s %s", p.Namespace, p.Name,
			p.Status.Reason, p.Status.Message)
	}

	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name != kubeletContainer {
			continue
		}

		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			msg := strings.TrimSpace(t.Message)
			if msg == "" {
				msg = t.Reason
			}
			return false, fmt.Errorf("container %s exited with status %d: %s",
				cs.Name, t.ExitCode, msg)
		}

		if w := cs.State.Waiting; w != nil && fatalWaiting[w.Reason] {
			return false, fmt.Errorf("container %s is not starting: %s: %s",
				cs.Name, w.Reason, w.Message)
		}

		return cs.Ready, nil
	}
	return false, nil
}

func (k *k8s) gensnap(ctx context.Context, ns, name string) (*vs.VolumeSnapshot, error) {
	snap := &vs.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "snap-" + name + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
			},
		},
		Spec: vs.VolumeSnapshotSpec{
			Source: vs.VolumeSnapshotSource{
				PersistentVolumeClaimName: &name,
			},
			VolumeSnapshotClassName: &k.volumeSnapshotClass,
		},
	}

	snap, err := k.snapClient.SnapshotV1().VolumeSnapshots(ns).Create(ctx, snap,
		metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	lw := &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = "metadata.name=" + snap.Name
			return k.snapClient.SnapshotV1().VolumeSnapshots(snap.Namespace).Watch(ctx, opts)
		},
	}

	evt, err := watchtools.Until(ctx, snap.ResourceVersion, lw, snapshotReady)
	if err != nil {
		k.delsnap(ctx, snap)
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, err
	}

	ready, ok := evt.Object.(*vs.VolumeSnapshot)
	if !ok {
		k.delsnap(ctx, snap)
		return nil, fmt.Errorf("unexpected object %T from the snapshot watch", evt.Object)
	}

	return ready, nil
}

func (k *k8s) delsnap(ctx context.Context, snap *vs.VolumeSnapshot) error {
	return k.snapClient.SnapshotV1().VolumeSnapshots(snap.ObjectMeta.Namespace).
		Delete(ctx, snap.ObjectMeta.Name, metav1.DeleteOptions{})
}

func (k *k8s) pvcFromSnap(ctx context.Context, ns string, snap *vs.VolumeSnapshot, orig *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
	apiGroup := "snapshot.storage.k8s.io"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "from-snap-",
			Namespace:    ns,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "VolumeSnapshot",
				Name:     snap.Name,
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: orig.Spec.Resources,
		},
	}

	return k.clientset.CoreV1().PersistentVolumeClaims(ns).
		Create(ctx, pvc, metav1.CreateOptions{})
}

func (k *k8s) getpvc(ctx context.Context, ns, name string) (*corev1.PersistentVolumeClaim, error) {
	return k.clientset.CoreV1().PersistentVolumeClaims(ns).
		Get(ctx, name, metav1.GetOptions{})
}

func (k *k8s) delpvc(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	return k.clientset.CoreV1().PersistentVolumeClaims(pvc.ObjectMeta.Namespace).
		Delete(ctx, pvc.Name, metav1.DeleteOptions{})
}

func (k *k8s) fsServer(ctx context.Context, op, ns string, pvc *corev1.PersistentVolumeClaim, readOnly bool, args ...string) (*tls.Certificate, *corev1.Pod, error) {
	cert, fp, err := mtls.Gencert()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate a certificate: %w", err)
	}

	args = append(args, "-p", "8080", "-peer", mtls.Fingerprint(fp))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "plakar-" + op + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
				"plakar.io/service":            uuid.NewString(),
			},
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "snap",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvc.Name,
						ReadOnly:  readOnly,
					},
				},
			}},
			Containers: []corev1.Container{{
				Name:  kubeletContainer,
				Image: k.kubeletImage,
				Args:  args,
				Ports: []corev1.ContainerPort{{
					Name:          "grpc",
					Protocol:      "TCP",
					ContainerPort: 8080,
				}},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "snap",
					MountPath: "/data",
				}},
				ReadinessProbe: &corev1.Probe{
					PeriodSeconds: 1,
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)},
					},
				},
			}},
		},
	}

	pod, err = k.clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, err
	}

	lw := &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = "metadata.name=" + pod.Name
			return k.clientset.CoreV1().Pods(pod.Namespace).Watch(ctx, opts)
		},
	}

	evt, err := watchtools.Until(ctx, pod.ResourceVersion, lw, podReady)
	if err != nil {
		k.delpod(ctx, pod)
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, err
	}

	ready, ok := evt.Object.(*corev1.Pod)
	if !ok {
		k.delpod(ctx, pod)
		return nil, nil, fmt.Errorf("unexpected object %T from the pod watch", evt.Object)
	}

	return &cert, ready, nil
}

func (k *k8s) delpod(ctx context.Context, pod *corev1.Pod) error {
	return k.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
}

func (k *k8s) serviceFor(ctx context.Context, pod *corev1.Pod) (*corev1.Service, error) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pod.Name + "-",
			Namespace:    pod.Namespace,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Name:       pod.Spec.Containers[0].Ports[0].Name,
				Protocol:   pod.Spec.Containers[0].Ports[0].Protocol,
				Port:       pod.Spec.Containers[0].Ports[0].ContainerPort,
				TargetPort: intstr.FromInt32(pod.Spec.Containers[0].Ports[0].ContainerPort),
			}},
			Selector: pod.ObjectMeta.Labels,
		},
	}

	svc, err := k.clientset.CoreV1().Services(pod.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create service: %w", err)
	}

	return svc, nil
}

func (k *k8s) delservice(ctx context.Context, svc *corev1.Service) error {
	return k.clientset.CoreV1().Services(svc.Namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{})
}

func progress(ctx context.Context, imp importer.Importer, fn func(<-chan *connectors.Record, chan<- *connectors.Result)) error {
	var (
		size    = 2
		records = make(chan *connectors.Record, size)
		retch   = make(chan struct{}, 1)
	)

	var results chan *connectors.Result
	if (imp.Flags() & location.FLAG_NEEDACK) != 0 {
		results = make(chan *connectors.Result, size)
	}

	go func() {
		fn(records, results)
		if results != nil {
			close(results)
		}
		close(retch)
	}()

	err := imp.Import(ctx, records, results)
	<-retch
	return err
}

func (k *k8s) consume(ctx context.Context, cert *tls.Certificate, dest, podpath string, Records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	cred := credentials.NewTLS(mtls.ClientTlsConfig(cert))

	client, err := grpc.NewClient(dest, grpc.WithTransportCredentials(cred))
	if err != nil {
		return fmt.Errorf("failed to create a grpc client for %s: %w", dest, err)
	}
	defer client.Close()

	opts := &connectors.Options{
		Hostname:        "plakar-pod",
		OperatingSystem: "linux",
		Architecture:    runtime.GOOS,
		CWD:             podpath,
		MaxConcurrency:  k.opts.MaxConcurrency,
	}

	importer, err := gimporter.NewImporter(ctx, client, opts, "fs", map[string]string{
		"location":         "fs://" + podpath,
		"dont_traverse_fs": "true",
	})
	if err != nil {
		return fmt.Errorf("failed to instantiate the importer: %w", err)
	}
	defer importer.Close(ctx)

	var done atomic.Uint64

	go func() {
		for range results {
			done.Add(1)
		}
	}()

	var total uint64
	err = progress(ctx, importer, func(records <-chan *connectors.Record, results chan<- *connectors.Result) {
		for record := range records {
			if record.Pathname == "/" {
				if results != nil {
					results <- record.Ok()
				} else {
					record.Close()
				}
				continue
			}

			newrecord := *record
			newrecord.Pathname = strings.TrimPrefix(record.Pathname, "/data")
			if newrecord.Pathname == "" {
				newrecord.Pathname = "/"
				newrecord.FileInfo.Lname = "/"
			}

			Records <- &newrecord
			total++
		}
	})
	if err != nil {
		return fmt.Errorf("failed to run the grpc importer: %w", err)
	}

	for {
		if total == done.Load() {
			return nil
		}
		time.Sleep(time.Second)
	}
}

func (k *k8s) urlFor(ctx context.Context, pod *corev1.Pod, svc *corev1.Service) (string, chan struct{}, error) {
	if k.portForward {
		u := k.clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(pod.Namespace).
			Name(pod.Name).
			SubResource("portforward").URL()

		transport, upgrader, err := spdy.RoundTripperFor(k.config)
		if err != nil {
			return "", nil, err
		}

		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", u)

		var (
			stopChan  = make(chan struct{}, 1)
			readyChan = make(chan struct{}, 1)
		)

		p := fmt.Sprintf(":%d", svc.Spec.Ports[0].Port)
		pf, err := portforward.New(dialer, []string{p}, stopChan, readyChan, io.Discard, io.Discard)
		if err != nil {
			close(stopChan)
			return "", nil, err
		}

		go pf.ForwardPorts()

		<-readyChan
		ports, err := pf.GetPorts()
		if err != nil {
			close(stopChan)
			return "", nil, err
		}

		return fmt.Sprintf("localhost:%d", ports[0].Local), stopChan, nil
	}

	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace,
		svc.Spec.Ports[0].Port), nil, nil
}

func (k *k8s) podBackup(ctx context.Context, cert *tls.Certificate, pod *corev1.Pod, svc *corev1.Service, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	url, stop, err := k.urlFor(ctx, pod, svc)
	if err != nil {
		return err
	}
	if stop != nil {
		defer close(stop)
	}

	return k.consume(ctx, cert, url, "/data", records, results)
}

func (k *k8s) podRestore(ctx context.Context, cert *tls.Certificate, pod *corev1.Pod, svc *corev1.Service, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	url, stop, err := k.urlFor(ctx, pod, svc)
	if err != nil {
		return err
	}
	if stop != nil {
		defer close(stop)
	}

	cred := credentials.NewTLS(mtls.ClientTlsConfig(cert))
	client, err := grpc.NewClient(url, grpc.WithTransportCredentials(cred))
	if err != nil {
		return fmt.Errorf("failed to create a grpc client for %s: %w", url, err)
	}
	defer client.Close()

	opts := &connectors.Options{
		Hostname:        "plakar-pod",
		OperatingSystem: "linux",
		Architecture:    runtime.GOOS,
		CWD:             "/data",
		MaxConcurrency:  k.opts.MaxConcurrency,
	}

	exporter, err := gexporter.NewExporter(ctx, client, opts, "fs", map[string]string{
		"location": "fs:///data",
	})
	if err != nil {
		return fmt.Errorf("failed to instantiate the exporter: %w", err)
	}
	defer exporter.Close(ctx)

	return exporter.Export(ctx, records, results)
}

func (k *k8s) backupPvc(ctx context.Context, ns, name string, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	var (
		pvc *corev1.PersistentVolumeClaim
		err error
	)

	switch k.proto {
	case "k8s+csi":
		orig, err := k.getpvc(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("failed to get pvc %s/%s: %w", ns, name, err)
		}

		snap, err := k.gensnap(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("failed to generate the snapshot: %w", err)
		}
		defer k.delsnap(ctx, snap)

		pvc, err = k.pvcFromSnap(ctx, ns, snap, orig)
		if err != nil {
			return fmt.Errorf("failed to generate the pvc from the snap: %w", err)
		}
		defer k.delpvc(ctx, pvc)

	case "k8s+pvc":
		pvc, err = k.getpvc(ctx, ns, name)
		if err != nil {
			return fmt.Errorf("failed to get PVC %s/%s: %w",
				ns, name, err)
		}

	default:
		return fmt.Errorf("unexpected protocol %q", k.proto)
	}

	cert, pod, err := k.fsServer(ctx, "backup", ns, pvc, true)
	if err != nil {
		return fmt.Errorf("failed to create the pod: %w", err)
	}
	defer k.delpod(ctx, pod)

	svc, err := k.serviceFor(ctx, pod)
	if err != nil {
		return fmt.Errorf("failed to create the service: %w", err)
	}
	defer k.delservice(ctx, svc)

	return k.podBackup(ctx, cert, pod, svc, records, results)
}

func (k *k8s) restorePvc(ctx context.Context, ns, name string, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	pvc, err := k.getpvc(ctx, ns, name)
	if err != nil {
		return fmt.Errorf("failed to get the PVC %s.%s: %w", ns, name, err)
	}

	cert, pod, err := k.fsServer(ctx, "restore", ns, pvc, false, "-export")
	if err != nil {
		return fmt.Errorf("failed to run the pod: %w", err)
	}
	defer k.delpod(ctx, pod)

	svc, err := k.serviceFor(ctx, pod)
	if err != nil {
		return fmt.Errorf("failed to create the service: %w", err)
	}
	defer k.delservice(ctx, svc)

	return k.podRestore(ctx, cert, pod, svc, records, results)
}
