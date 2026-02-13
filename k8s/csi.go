package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	// sftpImporter "github.com/PlakarKorp/integration-sftp/importer"
	// "github.com/PlakarKorp/kloset/connectors"
)

func isready(snap *vs.VolumeSnapshot) bool {
	return snap.Status != nil && snap.Status.ReadyToUse != nil && *snap.Status.ReadyToUse
}

func (k *k8s) gensnap(ctx context.Context, ns, name string) (*vs.VolumeSnapshot, error) {
	log.Println(">>>> in gensnap for", ns, name)
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

	log.Println("created", snap.Name)

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
			log.Printf("the watcher returned an object of an unknown type %t",
				evt.Object)
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
			log.Printf("the snapshot %s is ready!", snap.Name)
			break
		}
	}

	return snap, err
}

func (k *k8s) delsnap(ctx context.Context, snap *vs.VolumeSnapshot) error {
	log.Println("deleting snap", snap.Name)
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
				Kind:     snap.Kind,
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
	log.Println("deleting pvc", pvc.Name)
	return k.clientset.CoreV1().PersistentVolumeClaims(pvc.ObjectMeta.Namespace).
		Delete(ctx, pvc.Name, metav1.DeleteOptions{})
}

func (k *k8s) sftpServer(ctx context.Context, ns string, pvc *corev1.PersistentVolumeClaim) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "plakar-backup-",
			Namespace:    ns,
			Labels: map[string]string{
				"plakar.io/generated-resource": "true",
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
				Name:  "sftp",
				Image: "atmoz/sftp",
				Args:  []string{"chunky:ptarson:::/data"},
				Ports: []corev1.ContainerPort{{
					Name:          "ssh",
					ContainerPort: 22,
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

	log.Println("pod created", pod.Name)

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
				log.Printf("pods: the watcher returned an object of type %t",
					evt.Object)
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
	log.Println("deleting pod", pod.Name)
	return k.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
}

func (k *k8s) sftpBackup(ctx context.Context, pod *corev1.Pod) error {
	var url string
	if k.portForward {
		u := k.clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(pod.Namespace).
			Name(pod.Name).
			SubResource("portforward").URL()

		transport, upgrader, err := spdy.RoundTripperFor(k.config)
		if err != nil {
			return err
		}

		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", u)

		var (
			stopChan  = make(chan struct{}, 1)
			readyChan = make(chan struct{}, 1)
		)

		defer close(stopChan)

		pf, err := portforward.New(dialer, []string{":22"}, stopChan, readyChan, io.Discard, io.Discard)
		if err != nil {
			return err
		}

		go pf.ForwardPorts()

		<-readyChan
		ports, err := pf.GetPorts()
		if err != nil {
			return err
		}

		log.Printf("ports is %+v", ports)

		url = fmt.Sprintf("sftp://chunky@localhost:%d/", ports[0].Local)
	} else {
		url = fmt.Sprintf("sftp://%s.%s.svc.cluster.local:22", pod.Name, pod.Namespace)
	}

	log.Println("url is", url)

	// sftpImporter.NewImporter(ctx, &connectors.Options{
	// 	Hostname:        "",
	// 	OperatingSystem: "",
	// 	Architecture:    "",
	// 	CWD:             "",
	// 	MaxConcurrency:  0,
	// 	Excludes:        []string{},
	// 	Stdin:           nil,
	// 	Stdout:          nil,
	// 	Stderr:          nil,
	// }, "sftp", )

	log.Println("created everything, now waiting")
	fp, err := os.Open("/dev/tty")
	if err != nil {
		log.Println("failed to open /dev/tty:", err)
	} else {
		var buf [1]byte
		fp.Read(buf[:])
		fp.Close()
	}

	return nil
}

func (k *k8s) backupPvc(ctx context.Context, ns, name string) error {
	snap, err := k.gensnap(ctx, ns, name)
	if err != nil {
		log.Println("failed to generate the snapshot:", err)
		return err
	}

	pvc, err := k.pvcFromSnap(ctx, ns, snap)
	if err != nil {
		k.delsnap(ctx, snap)
		log.Println("failed to generate the pvc from the snap:", err)
		return err
	}

	pod, err := k.sftpServer(ctx, ns, pvc)
	if err != nil {
		k.delpvc(ctx, pvc)
		k.delsnap(ctx, snap)
		log.Println("failed to generate pod from the pvc:", err)
		return err
	}

	err = k.sftpBackup(ctx, pod)
	if err != nil {
		log.Println("failed to backup the pod:", err)
	}

	if err := k.delpod(ctx, pod); err != nil {
		log.Printf("failed to delete pod %s/%s: %s", pod.ObjectMeta.Namespace,
			pvc.ObjectMeta.Name, err)
	}

	if err := k.delpvc(ctx, pvc); err != nil {
		log.Printf("failed to delete PVC %s/%s: %s", pvc.ObjectMeta.Namespace,
			pvc.ObjectMeta.Name, err)
	}

	if err := k.delsnap(ctx, snap); err != nil {
		log.Printf("failed to delete VolumeSnapshot %s/%s: %s", snap.ObjectMeta.Namespace,
			snap.ObjectMeta.Name, err)
	}

	return err
}
