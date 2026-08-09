# kubernetes integration

This integration allows [plakar][plakar] to backup and restore
[kubernetes][kubernetes] resources and PersistentVolumes, both via the
CSI driver snapshot feature (preferred) and without.

[plakar]:     https://plakar.io/
[kubernetes]: https://kubernetes.io/


## Configuration

- `kubeconfig_file`: optional, point to a kube config file.  Defaults to `~/.kube/config`.
- `kubeconfig`: optional, content of a kube config passed inline.  Takes precedence over `kubeconfig_file`.
- `kubelet_image`: optional, used only for PVC backups.  Defaults to a recent version of the kubelet image.
- `labels`: optional, used only for configuration backup.  Limits the manifests to backup to the ones matching the given labels.
- `volume_snapshot_class`: required for CSI-based PVC backups.  It's the volume snapshot class to use.

## Examples

Backup all the resources applied to a kubernetes cluster:

	$ plakar backup k8s:/

Same as before but only for the resources in the `foo` namespace:

	$ plakar backup k8s:/foo

Restore all the `StatefulSet`s in the `foo` namespace:

	$ plakar restore -to k8s: abcd:/foo/apps/StatefulSet

Backup the PVC `my-pvc` in the `storage` namespace:

	$ plakar backup -o volume_snapshot_class=my-snapclass k8s+csi:/storage/my-pvc

Restore inside a new, pristine, PersistentVolumeClaim:

	$ kubectl create -f -
	apiVersion: v1
	kind: PersistentVolumeClaim
	metadata:
	  name: pristine
	  namespace: storage
	spec:
	  resources:
		requests:
		 storage: 1Gi
	  accessModes:
	   - ReadWriteOnce
	$ plakar restore -to k8s+pvc:/storage/pristine abcdef:

of course it's possible to restore the data inside an already existing PVC as well.
