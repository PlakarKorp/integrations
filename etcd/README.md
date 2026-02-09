# etcd integration

[etcd][etcd] is a distributed, reliable key-value store.  This
integration allows [plakar][plakar] to take snapshot of an etcd
cluster for later recovery.

[etcd]:   https://etcd.io/
[plakar]: https://plakar.io/


## Configuration

The configuration parameters are as follows:

- location: use the `etcd`, `etcd+http` or `etcd+https` protocol, plus
  the hostname and optional port to a node of the etcd cluster.
- endpoints (optional): comma-separated list of node endpoints to
  connect to, takes priority over the location.
- username and password (optional)


## Example

Backup etcd by connecting to a node over http without authentication:

	$ plakar backup etcd://node1:2379


Like the previous but using HTTPS and authentication:

	$ plakar backup -o username=chunky.ptarson -o password=secure! etcd+https://node1:2379

Finally, passing a list of nodes to connect to:

	$ plakar backup -o endpoints=http://node1:2379,http://node2:2379 etcd://


## Recovering

etcd doesn't provide a way to restore using the APIs: the way of doing
so is to restore the snapshot taken by plakar on the disk and then use
etcdutl to provision a new etcd data directory.

Please refer to upstream documentation: <https://etcd.io/docs/latest/op-guide/recovery/>.
