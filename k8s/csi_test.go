package k8s

import (
	"errors"
	"testing"

	"github.com/PlakarKorp/integrations/k8s/mtls"
	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	snapfake "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned/fake"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func pod(phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "plakar-backup-x", Namespace: "ns"},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
		},
	}
}

func container(name string, state corev1.ContainerState, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: state, Ready: ready}
}

func waiting(reason, message string) corev1.ContainerState {
	return corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message},
	}
}

func terminated(code int32, reason, message string) corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			ExitCode: code, Reason: reason, Message: message,
		},
	}
}

func running() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
}

func volumesnap(status *vs.VolumeSnapshotStatus) *vs.VolumeSnapshot {
	return &vs.VolumeSnapshot{
		Status: status,
	}
}

func readyToUse(x bool) *vs.VolumeSnapshotStatus {
	return &vs.VolumeSnapshotStatus{
		ReadyToUse: new(x),
	}
}

func volfailed(msg string) *vs.VolumeSnapshotStatus {
	return &vs.VolumeSnapshotStatus{
		Error: &vs.VolumeSnapshotError{
			Message: new(msg),
		},
	}
}

func TestSnapshotReady(t *testing.T) {
	suite := []struct {
		name    string
		evt     watch.Event
		want    bool
		wantErr string
	}{
		{
			name: "ready",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: volumesnap(readyToUse(true)),
			},
			want: true,
		},
		{
			name: "not ready yet",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: volumesnap(readyToUse(false)),
			},
			want: false,
		},
		{
			name: "failed",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: volumesnap(volfailed("failed to attach")),
			},
			want:    false,
			wantErr: "failed to attach",
		},
		{
			name: "error",
			evt: watch.Event{
				Type:   watch.Error,
				Object: &metav1.Status{Message: "invalid frobnication"},
			},
			want:    false,
			wantErr: "invalid frobnication",
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			got, err := snapshotReady(test.evt)

			if test.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.wantErr)
				require.False(t, got, "a failing snapshot is never okay")
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestPodReady(t *testing.T) {
	const kl = kubeletContainer
	suite := []struct {
		name    string
		evt     watch.Event
		want    bool
		wantErr string
	}{
		{
			name: "serving",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodRunning, container(kl, running(), true)),
			},
			want: true,
		},
		{
			name: "running but not ready yet",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodRunning, container(kubeletContainer, running(), false)),
			},
		},
		{
			name: "still pending, no container statuses",
			evt: watch.Event{
				Type:   watch.Added,
				Object: pod(corev1.PodPending),
			},
		},
		{
			name: "pulling the image is not fatal",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodPending, container(kubeletContainer, waiting("ContainerCreating", ""), false)),
			},
		},
		{
			name: "a transient image pull error is not fatal",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodPending, container(kubeletContainer, waiting("ErrImagePull", "quay.io timed out"), false)),
			},
		},
		{
			name: "another container's status is ignored",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodRunning, container("something-else", running(), true)),
			},
		},
		{
			name: "clean exit is not fatal today",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodSucceeded, container(kubeletContainer, terminated(0, "Completed", ""), false)),
			},
			wantErr: "container kubelet exited with status 0: Completed",
		},
		{
			name: "watch error carries the status",
			evt: watch.Event{
				Type:   watch.Error,
				Object: &metav1.Status{Message: "too old resource version"},
			},
			wantErr: "too old resource version",
		},
		{
			name: "deleted while starting",
			evt: watch.Event{
				Type:   watch.Deleted,
				Object: pod(corev1.PodRunning, container(kubeletContainer, running(), true)),
			},
			wantErr: "was deleted while starting",
		},
		{
			name: "pod failed",
			evt: watch.Event{
				Type:   watch.Modified,
				Object: pod(corev1.PodFailed),
			},
			wantErr: "failed",
		},
		{
			name: "rejected its arguments",
			evt: watch.Event{
				Type: watch.Modified,
				Object: pod(corev1.PodPending,
					container(
						kubeletContainer,
						terminated(2, "Error", "flag provided but not defined: -peer"),
						false,
					)),
			},
			wantErr: "flag provided but not defined: -peer",
		},
		{
			name: "exited with no termination message falls back to the reason",
			evt: watch.Event{
				Type: watch.Modified,
				Object: pod(corev1.PodPending,
					container(kubeletContainer, terminated(1, "Error", ""), false)),
			},
			wantErr: "exited with status 1: Error",
		},
		{
			name: "crash looping",
			evt: watch.Event{
				Type: watch.Modified,
				Object: pod(corev1.PodPending,
					container(kubeletContainer, waiting("CrashLoopBackOff", "back-off 40s"), false)),
			},
			wantErr: "CrashLoopBackOff",
		},
		{
			name: "image cannot be pulled",
			evt: watch.Event{
				Type: watch.Modified,
				Object: pod(corev1.PodPending,
					container(kubeletContainer, waiting("ImagePullBackOff", "back-off pulling"), false)),
			},
			wantErr: "ImagePullBackOff",
		},
		{
			name: "kubelet refused the spec",
			evt: watch.Event{
				Type: watch.Modified,
				Object: pod(corev1.PodPending,
					container(kubeletContainer, waiting("CreateContainerConfigError", "non-numeric user"), false)),
			},
			wantErr: "CreateContainerConfigError",
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			got, err := podReady(test.evt)

			if test.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.wantErr)
				require.False(t, got, "a failing pod is never serving")
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestPodReadyIgnoresOtherObjects(t *testing.T) {
	got, err := podReady(watch.Event{
		Type:   watch.Modified,
		Object: &corev1.Service{},
	})

	require.NoError(t, err)
	require.False(t, got)
}

func TestPodReadyErrorEventAlwaysFails(t *testing.T) {
	_, err := podReady(watch.Event{Type: watch.Error, Object: &corev1.Pod{}})

	require.Error(t, err)
}

func newCsiTestK8s(objs ...runtime.Object) *k8s {
	return &k8s{
		clientset:  k8sfake.NewSimpleClientset(objs...),
		snapClient: snapfake.NewSimpleClientset(),
	}
}

// withGeneratedResourceVersion makes the fake snapshot clientset behave like
// a real apiserver on Create: assigning a name (from GenerateName) and a
// ResourceVersion.  The generated fake tracker doesn't, and watchtools.Until 
// refuses to start a watch from an empty resource version.
func withGeneratedResourceVersion(snapClient *snapfake.Clientset) {
	snapClient.PrependReactor("create", "volumesnapshots", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(metav1.Object)
		if obj.GetName() == "" {
			obj.SetName(obj.GetGenerateName() + "generated")
		}
		obj.SetResourceVersion("1")
		return false, nil, nil
	})
}

// singleEventWatchReactor replies to every Watch() call with a watcher that
// has the event queued up.  Plugged this way because watchtools.Until calls
// Watch() more than once from different goroutines.
func singleEventWatchReactor(evt watch.Event) clienttesting.WatchReactionFunc {
	return func(clienttesting.Action) (bool, watch.Interface, error) {
		w := watch.NewFakeWithChanSize(1, false)
		switch evt.Type {
		case watch.Added:
			w.Add(evt.Object)
		case watch.Modified:
			w.Modify(evt.Object)
		case watch.Deleted:
			w.Delete(evt.Object)
		case watch.Error:
			w.Error(evt.Object)
		}
		return true, w, nil
	}
}

func TestCloneSize(t *testing.T) {
	pvcWithSize := func(size string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(size),
					},
				},
			},
		}
	}

	suite := []struct {
		name string
		orig *corev1.PersistentVolumeClaim
		snap *vs.VolumeSnapshot
		want string
	}{
		{
			name: "no snapshot status uses the orig size",
			orig: pvcWithSize("10Gi"),
			snap: &vs.VolumeSnapshot{},
			want: "10Gi",
		},
		{
			name: "status without restore size uses the orig size",
			orig: pvcWithSize("5Gi"),
			snap: &vs.VolumeSnapshot{Status: &vs.VolumeSnapshotStatus{}},
			want: "5Gi",
		},
		{
			name: "restore size larger than orig wins",
			orig: pvcWithSize("5Gi"),
			snap: &vs.VolumeSnapshot{Status: &vs.VolumeSnapshotStatus{RestoreSize: new(resource.MustParse("10Gi"))}},
			want: "10Gi",
		},
		{
			name: "restore size smaller than orig keeps orig",
			orig: pvcWithSize("10Gi"),
			snap: &vs.VolumeSnapshot{Status: &vs.VolumeSnapshotStatus{RestoreSize: new(resource.MustParse("5Gi"))}},
			want: "10Gi",
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			got := cloneSize(test.orig, test.snap)
			want := resource.MustParse(test.want)
			require.Zero(t, got.Cmp(want), "got %s, want %s", got.String(), want.String())
		})
	}
}

func TestGetPvc(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		k := newCsiTestK8s(pvcObj("ns", "data"))

		pvc, err := k.getpvc(t.Context(), "ns", "data")
		require.NoError(t, err)
		require.Equal(t, "data", pvc.Name)
	})

	t.Run("not found propagates the api error", func(t *testing.T) {
		k := newCsiTestK8s()

		_, err := k.getpvc(t.Context(), "ns", "missing")
		require.Error(t, err)
	})

	t.Run("rejects raw block volumes", func(t *testing.T) {
		pvc := pvcObj("ns", "block")
		pvc.Spec.VolumeMode = new(corev1.PersistentVolumeBlock)
		k := newCsiTestK8s(pvc)

		_, err := k.getpvc(t.Context(), "ns", "block")
		require.ErrorContains(t, err, "raw block volume")
	})
}

func TestDelPvc(t *testing.T) {
	t.Run("deletes the pvc", func(t *testing.T) {
		pvc := pvcObj("ns", "data")
		k := newCsiTestK8s(pvc)

		k.delpvc(t.Context(), pvc)

		_, err := k.clientset.CoreV1().PersistentVolumeClaims("ns").Get(t.Context(), "data", metav1.GetOptions{})
		require.True(t, apierrors.IsNotFound(err))
	})

	t.Run("delete error is only logged", func(t *testing.T) {
		pvc := pvcObj("ns", "data")
		k := newCsiTestK8s(pvc)
		k.clientset.(*k8sfake.Clientset).PrependReactor("delete", "persistentvolumeclaims",
			func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("boom")
			})

		require.NotPanics(t, func() { k.delpvc(t.Context(), pvc) })
	})
}

func TestDelPod(t *testing.T) {
	testPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns"}}

	t.Run("deletes the pod", func(t *testing.T) {
		k := newCsiTestK8s(testPod)

		k.delpod(t.Context(), testPod)

		_, err := k.clientset.CoreV1().Pods("ns").Get(t.Context(), "pod1", metav1.GetOptions{})
		require.True(t, apierrors.IsNotFound(err))
	})

	t.Run("delete error is only logged", func(t *testing.T) {
		k := newCsiTestK8s(testPod)
		k.clientset.(*k8sfake.Clientset).PrependReactor("delete", "pods",
			func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("boom")
			})

		require.NotPanics(t, func() { k.delpod(t.Context(), testPod) })
	})
}

func TestPvcFromSnap(t *testing.T) {
	k := newCsiTestK8s()

	orig := &corev1.PersistentVolumeClaim{
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: new("fast"),
			VolumeMode:       new(corev1.PersistentVolumeFilesystem),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
	snap := &vs.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "snap-1"},
		Status: &vs.VolumeSnapshotStatus{
			RestoreSize: new(resource.MustParse("10Gi")),
		},
	}

	pvc, err := k.pvcFromSnap(t.Context(), "ns", snap, orig)
	require.NoError(t, err)

	require.Equal(t, "ns", pvc.Namespace)
	require.NotNil(t, pvc.Spec.DataSource)
	require.Equal(t, "VolumeSnapshot", pvc.Spec.DataSource.Kind)
	require.Equal(t, "snap-1", pvc.Spec.DataSource.Name)
	require.Equal(t, "snapshot.storage.k8s.io", *pvc.Spec.DataSource.APIGroup)
	require.Equal(t, orig.Spec.AccessModes, pvc.Spec.AccessModes)
	require.Equal(t, orig.Spec.StorageClassName, pvc.Spec.StorageClassName)
	require.Equal(t, orig.Spec.VolumeMode, pvc.Spec.VolumeMode)

	got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	want := resource.MustParse("10Gi")
	require.Zero(t, got.Cmp(want), "got %s, want %s", got.String(), want.String())
}

func TestGensnap(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		snapClient := snapfake.NewSimpleClientset()
		withGeneratedResourceVersion(snapClient)
		snapClient.PrependWatchReactor("volumesnapshots", singleEventWatchReactor(watch.Event{
			Type: watch.Modified,
			Object: &vs.VolumeSnapshot{
				ObjectMeta: metav1.ObjectMeta{Name: "snap-pvc1-x", Namespace: "ns", ResourceVersion: "2"},
				Status:     readyToUse(true),
			},
		}))

		k := &k8s{snapClient: snapClient, volumeSnapshotClass: "csi-class"}

		res, err := k.gensnap(t.Context(), "ns", "pvc1")
		require.NoError(t, err)
		require.NotNil(t, res.Status)
		require.True(t, *res.Status.ReadyToUse)

		list, err := snapClient.SnapshotV1().VolumeSnapshots("ns").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)
		require.Equal(t, "pvc1", *list.Items[0].Spec.Source.PersistentVolumeClaimName)
		require.Equal(t, "csi-class", *list.Items[0].Spec.VolumeSnapshotClassName)
	})

	t.Run("failed status deletes the snapshot and returns the error", func(t *testing.T) {
		snapClient := snapfake.NewSimpleClientset()
		withGeneratedResourceVersion(snapClient)
		snapClient.PrependWatchReactor("volumesnapshots", singleEventWatchReactor(watch.Event{
			Type: watch.Modified,
			Object: &vs.VolumeSnapshot{
				ObjectMeta: metav1.ObjectMeta{Name: "snap-pvc1-x", Namespace: "ns", ResourceVersion: "2"},
				Status:     volfailed("failed to attach"),
			},
		}))

		var deleted bool
		snapClient.PrependReactor("delete", "volumesnapshots", func(clienttesting.Action) (bool, runtime.Object, error) {
			deleted = true
			return false, nil, nil
		})

		k := &k8s{snapClient: snapClient, volumeSnapshotClass: "csi-class"}

		_, err := k.gensnap(t.Context(), "ns", "pvc1")
		require.ErrorContains(t, err, "failed to attach")
		require.True(t, deleted, "gensnap must delete the snapshot when it failed")
	})

	// don't attempt to test watch.Error because watchtools.Until uses a
	// RetryWatcher that would end up retrying forever.
}

func TestDelsnap(t *testing.T) {
	newSnap := func() *vs.VolumeSnapshot {
		return &vs.VolumeSnapshot{ObjectMeta: metav1.ObjectMeta{Name: "snap1", Namespace: "ns"}}
	}

	t.Run("deletes the snapshot", func(t *testing.T) {
		snap := newSnap()
		snapClient := snapfake.NewSimpleClientset(snap)
		k := &k8s{snapClient: snapClient}

		k.delsnap(t.Context(), snap)

		_, err := snapClient.SnapshotV1().VolumeSnapshots("ns").Get(t.Context(), "snap1", metav1.GetOptions{})
		require.True(t, apierrors.IsNotFound(err))
	})

	t.Run("delete error is only logged", func(t *testing.T) {
		snap := newSnap()
		snapClient := snapfake.NewSimpleClientset(snap)
		snapClient.PrependReactor("delete", "volumesnapshots", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
		k := &k8s{snapClient: snapClient}

		require.NotPanics(t, func() { k.delsnap(t.Context(), snap) })
	})
}

func TestPodTrouble(t *testing.T) {
	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}

	t.Run("collects unique warning events", func(t *testing.T) {
		k := newCsiTestK8s(testPod)
		k.clientset.(*k8sfake.Clientset).PrependReactor("list", "events",
			func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, &corev1.EventList{Items: []corev1.Event{
					{Type: corev1.EventTypeWarning, Reason: "FailedMount", Message: "timeout"},
					{Type: corev1.EventTypeWarning, Reason: "FailedMount", Message: "timeout"},
					{Type: corev1.EventTypeNormal, Reason: "Scheduled", Message: "assigned"},
				}}, nil
			})

		got := k.podTrouble(t.Context(), testPod)
		require.Equal(t, "FailedMount: timeout", got)
	})

	t.Run("falls back to the phase when there are no warnings", func(t *testing.T) {
		k := newCsiTestK8s(testPod)
		k.clientset.(*k8sfake.Clientset).PrependReactor("list", "events",
			func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, &corev1.EventList{}, nil
			})

		got := k.podTrouble(t.Context(), testPod)
		require.Contains(t, got, "no warnings")
	})

	t.Run("read failure is reported instead of panicking", func(t *testing.T) {
		k := newCsiTestK8s(testPod)
		k.clientset.(*k8sfake.Clientset).PrependReactor("list", "events",
			func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("apiserver unreachable")
			})

		got := k.podTrouble(t.Context(), testPod)
		require.Contains(t, got, "apiserver unreachable")
	})
}

// fsServerTestK8s wires a fake clientset so that fsServer's create, watch,
// and peerFingerprint's log read all succeed: pod creation gets a generated
// name/resourceVersion (the fake tracker doesn't assign either), the watch
// immediately reports the pod ready, and the log stream announces a real
// pinned public key.
func fsServerTestK8s(t *testing.T, pvc *corev1.PersistentVolumeClaim) *k8s {
	t.Helper()

	k := newCsiTestK8s(pvc)
	cs := k.clientset.(*k8sfake.Clientset)

	cs.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(metav1.Object)
		if obj.GetName() == "" {
			obj.SetName(obj.GetGenerateName() + "generated")
		}
		obj.SetResourceVersion("1")
		return false, nil, nil
	})

	_, fp, err := mtls.Gencert()
	require.NoError(t, err)
	pubkeyLine := []byte("plakar-pubkey: " + mtls.Fingerprint(fp) + "\n")

	cs.PrependReactor("get", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "log" {
			return false, nil, nil
		}
		return true, &runtime.Unknown{Raw: pubkeyLine}, nil
	})

	cs.PrependWatchReactor("pods", singleEventWatchReactor(watch.Event{
		Type: watch.Modified,
		Object: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "plakar-backup-x", Namespace: "ns", ResourceVersion: "2"},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{container(kubeletContainer, running(), true)},
			},
		},
	}))

	return k
}

func TestFsServer(t *testing.T) {
	t.Run("filesystem mode mounts the pvc", func(t *testing.T) {
		pvc := pvcObj("ns", "data")
		k := fsServerTestK8s(t, pvc)

		fp, err := k.fsServer(t.Context(), "backup", "ns", pvc, true)
		require.NoError(t, err)
		require.False(t, fp.block)

		list, err := k.clientset.CoreV1().Pods("ns").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)

		c := list.Items[0].Spec.Containers[0]
		require.Empty(t, c.VolumeDevices)
		require.Equal(t, []corev1.VolumeMount{{Name: "snap", MountPath: "/data"}}, c.VolumeMounts)
	})

	t.Run("block mode exposes a raw device", func(t *testing.T) {
		pvc := pvcObj("ns", "block")
		pvc.Spec.VolumeMode = new(corev1.PersistentVolumeBlock)
		k := fsServerTestK8s(t, pvc)

		fp, err := k.fsServer(t.Context(), "backup", "ns", pvc, true)
		require.NoError(t, err)
		require.True(t, fp.block)

		list, err := k.clientset.CoreV1().Pods("ns").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)

		c := list.Items[0].Spec.Containers[0]
		require.Empty(t, c.VolumeMounts)
		require.Equal(t, []corev1.VolumeDevice{{Name: "snap", DevicePath: blockDevicePath}}, c.VolumeDevices)
	})
}
