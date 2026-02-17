# kubernetes integration

This integration allows [plakar][plakar] to backup and restore
[kubernetes][kubernetes] resources and PersistentVolumes backed by CSI
drivers.

[plakar]:     https://plakar.io/
[kubernetes]: https://kubernetes.io/


## Configuration

- `volume_snapshot_class`: required for PVC backups.  It's the volume snapshot class to use.
- `kubelet_image`: optional, used only for PVC backups.  Defaults to a recent version of the kubelet image.

## Examples

Run a proxy to access the kubernetes cluster:

	$ kubectl proxy
	Starting to serve on 127.0.0.1:8001

Backup all the resources applied to a kubernetes cluster:

	$ plakar backup k8s://localhost:8001

Same as before but only for the resources in the `foo` namespace:

	$ plakar backup k8s://localhost:8001/foo

Restore all the `StatefulSet`s in the `foo` namespace:

	$ plakar restore -to k8s://localhost:8001 abcd:/foo/apps/StatefulSet

Backup the PVC `my-pvc` in the `storage` namespace:

	$ plakar backup -o volume_snapshot_class=my-snapclass k8s+csi://localhost:8001/storage/my-pvc

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
	$ plakar restore k8s+csi://localhost:8001/storage/pristine

of course it's possible to restore the data inside an already existing PVC as well.
