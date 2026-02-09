# kubernetes integration

This integration allows [plakar][plakar] to backup and restore
[kubernetes][kubernetes] resources.

[plakar]:     https://plakar.io/
[kubernetes]: https://kubernetes.io/


## Configuration

None.


## Examples

Run a proxy to access the kubernetes cluster:

	$ kubectl proxy
	Starting to serve on 127.0.0.1:8001

Backup all the resources applied to a kubernetes cluster:

	$ plakar backup k8s://localhost:8001

Restore all the `StatefulSet`s in the `foo` namespace:

	$ plakar restore -to k8s://localhost:8001 abcd:/foo/apps/StatefulSet
