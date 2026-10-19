FROM alpine:3.20
RUN apk add --no-cache rsync \
 && mkdir -p /data/dir/sub \
 && echo hello > /data/hello.txt \
 && head -c 3000000 /dev/urandom > /data/dir/big.bin \
 && echo x > /data/dir/sub/x \
 && ln -s ../big.bin /data/dir/sub/symlink \
 && echo star > '/data/a*b' \
 && echo backslash > '/data/a\b' \
 && mkdir '/data/glob[1]' /data/glob1 \
 && echo bracket > '/data/glob[1]/f' \
 && echo decoy > /data/glob1/decoy \
 && adduser -D -u 1234 alice && chown alice /data/hello.txt \
 && echo 'bob:secret' > /etc/rsyncd.secrets && chmod 600 /etc/rsyncd.secrets \
 && mkdir /restore
# pub sends user names, priv (chrooted) only numeric ids. restore is writable,
# chrooted to keep symlink targets as they are.
RUN printf '%s\n' 'uid = root' 'gid = root' \
  '[pub]' 'path = /data' 'use chroot = no' 'numeric ids = no' \
  '[priv]' 'path = /data' 'use chroot = yes' 'auth users = bob' 'secrets file = /etc/rsyncd.secrets' \
  '[restore]' 'path = /restore' 'use chroot = yes' 'read only = no' \
  > /etc/rsyncd.conf
CMD ["rsync", "--daemon", "--no-detach", "--log-file=/dev/stderr"]
