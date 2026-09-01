package k8s

import (
	"testing"

	vs "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
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
