# Google Cloud Storage Integration

The **Plakar Google Cloud Storage (GCS) Integration** allows to back up,
restore to, and/or host your kloset repository on a google cloud bucket.


## Configuration

If possible, the integration will auto-authenticate itself, for e.g. if
you have the gcloud cli installed locally.  Otherwise, a service
account file can be explicitly provided.

The supported configuration options are:

- `credentials_file`: path to a JSON credentials file
- `credentials_json`: an in-line credentials JSON
- `endpoint`: to override the endpoint used, mostly for debug
- `no_auth`: to avoid authentication, useful to backup a public bucket
  for example


## Example usage

```sh
# back up a bucket
$ plakar backup gcs://bucket_name

# restore the snapshot "abc" to a bucket
$ plakar restore -to gcs://bucket_name abc

# create a kloset repository to store your backups on a bucket
$ plakar at gcs://bucket_name create
```
