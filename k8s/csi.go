package k8s

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	gimporter "github.com/PlakarKorp/integration-grpc/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/google/uuid"
	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

func isready(snap *vs.VolumeSnapshot) bool {
	return snap.Status != nil && snap.Status.ReadyToUse != nil && *snap.Status.ReadyToUse
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
			VolumeSnapshotClassName: &k.volumeSnapshotClassName,
		},
	}

	snap, err := k.snapClient.SnapshotV1().VolumeSnapshots(ns).Create(ctx, snap,
		metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	w, err := k.snapClient.SnapshotV1().VolumeSnapshots(ns).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		k.delsnap(ctx, snap)
		return nil, err
	}

	defer w.Stop()
	for {
		var evt watch.Event
		var ok bool
		select {
		case evt, ok = <-w.ResultChan():
			if !ok {
				return snap, err
			}
		case <-ctx.Done():
			k.delsnap(ctx, snap)
			return nil, ctx.Err()
		}

		if evt.Type == watch.Error {
			k.delsnap(ctx, snap)
			return nil, fmt.Errorf("watch failed")
		}

		if evt.Type != watch.Modified {
			continue
		}

		s, ok := evt.Object.(*vs.VolumeSnapshot)
		if !ok {
			continue
		}

		if s.Name != snap.Name {
			continue
		}

		if s.Status != nil && s.Status.Error != nil && s.Status.Error.Message != nil {
			k.delsnap(ctx, s)
			return nil, fmt.Errorf("%s", *s.Status.Error.Message)
		}

		if isready(s) {
			snap = s
			break
		}
	}

	return snap, err
}

func (k *k8s) delsnap(ctx context.Context, snap *vs.VolumeSnapshot) error {
	return k.snapClient.SnapshotV1().VolumeSnapshots(snap.ObjectMeta.Namespace).
		Delete(ctx, snap.ObjectMeta.Name, metav1.DeleteOptions{})
}

func (k *k8s) pvcFromSnap(ctx context.Context, ns string, snap *vs.VolumeSnapshot) (*corev1.PersistentVolumeClaim, error) {
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
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					"storage": resource.MustParse("1Gi"),
				},
			},
		},
	}

	return k.clientset.CoreV1().PersistentVolumeClaims(ns).
		Create(ctx, pvc, metav1.CreateOptions{})
}

func (k *k8s) delpvc(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	return k.clientset.CoreV1().PersistentVolumeClaims(pvc.ObjectMeta.Namespace).
		Delete(ctx, pvc.Name, metav1.DeleteOptions{})
}

func (k *k8s) fsServer(ctx context.Context, ns string, pvc *corev1.PersistentVolumeClaim) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "plakar-backup-",
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
						ReadOnly:  true,
					},
				},
			}},
			Containers: []corev1.Container{{
				Name:  "kubelet",
				Image: k.kubeletImage,
				Args:  []string{"-p", "8080"},
				Ports: []corev1.ContainerPort{{
					Name:          "grpc",
					Protocol:      "TCP",
					ContainerPort: 8080,
				}},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "snap",
					MountPath: "/data",
				}},
			}},
		},
	}

	pod, err := k.clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	w, err := k.clientset.CoreV1().Pods(pod.Namespace).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		k.delpod(ctx, pod)
		return nil, err
	}

	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			k.delpod(ctx, pod)
			return nil, ctx.Err()

		case evt, ok := <-w.ResultChan():
			if !ok {
				return pod, nil
			}

			if evt.Type == watch.Error {
				k.delpod(ctx, pod)
				return nil, fmt.Errorf("watch failed")
			}

			if evt.Type != watch.Modified {
				continue
			}

			p, ok := evt.Object.(*corev1.Pod)
			if !ok {
				continue
			}

			if p.Name != pod.Name {
				continue
			}

			if len(p.Status.ContainerStatuses) > 0 && p.Status.ContainerStatuses[0].Ready {
				return p, nil
			}
		}
	}
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

func (k *k8s) consume(ctx context.Context, dest, podpath string, Records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	client, err := grpc.NewClient(dest, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

func (k *k8s) podBackup(ctx context.Context, pod *corev1.Pod, svc *corev1.Service, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	url, stop, err := k.urlFor(ctx, pod, svc)
	if err != nil {
		return err
	}
	if stop != nil {
		defer close(stop)
	}

	return k.consume(ctx, url, "/data", records, results)
}

func (k *k8s) backupPvc(ctx context.Context, ns, name string, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	snap, err := k.gensnap(ctx, ns, name)
	if err != nil {
		return fmt.Errorf("failed to generate the snapshot: %w", err)
	}
	defer k.delsnap(ctx, snap)

	pvc, err := k.pvcFromSnap(ctx, ns, snap)
	if err != nil {
		return fmt.Errorf("failed to generate the pvc from the snap: %w", err)
	}
	defer k.delpvc(ctx, pvc)

	pod, err := k.fsServer(ctx, ns, pvc)
	if err != nil {
		return fmt.Errorf("failed to create the pod: %w", err)
	}
	defer k.delpod(ctx, pod)

	svc, err := k.serviceFor(ctx, pod)
	if err != nil {
		return fmt.Errorf("failed to create the service: %w", err)
	}
	defer k.delservice(ctx, svc)

	return k.podBackup(ctx, pod, svc, records, results)
}
