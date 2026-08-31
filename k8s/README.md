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


## Permissions

[`rbac.yaml`](rbac.yaml) carries the roles the integration needs, split in
four: PVC data (`k8s+csi` and `k8s+pvc`), manifest backup (`k8s:`), manifest
restore, and the inventory.  They differ enormously in scope, so apply only the
ones you actually use: a single account holding all is basically a cluster-admin.

Manifest backup might deserves a second look before granting it.  The walk asks
the discovery API for every listable resource type and lists them all, Secrets
included, so anyone who can run that backup, or read the snapshot, can read
every Secret in the cluster.

The `*/*` rule in `rbac.yaml` is what a *complete* manifest backup needs, not a
hard requirement: grant less and the backup still runs, recording every
resource type it was refused as an error instead of failing.  That is a
reasonable way to keep plakar away from Secrets.

The file is written for plakar running inside the cluster.  Driving it from
outside with a kubeconfig needs three changes:

- the `plakar` namespace and the ServiceAccounts are not used at all;
- every binding's subject becomes whatever identity the kubeconfig
  authenticates as, rather than a ServiceAccount:

		subjects:
		  - kind: User
		    name: plakar
		    apiGroup: rbac.authorization.k8s.io

- `plakar-pvc-portforward` has to be applied too, because the connection to
  the pod is tunnelled through the apiserver instead of going over the pod
  network.

RBAC has no schema validation, and as a cluster admin every check you run comes
back allowed, so verify against the identity the rules are written for:

	$ kubectl auth can-i list deployments.apps \
	    --as=system:serviceaccount:plakar:plakar-resources-backup


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
