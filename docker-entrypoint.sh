#!/bin/sh
# The data volume is mounted by the platform as root. Take ownership once,
# then drop to the service user for the whole life of the process: the
# server never runs as root, and the volume is writable by the one user
# that needs it.
set -e
if [ "$(id -u)" = "0" ]; then
  mkdir -p /data
  if [ "$(stat -c %u /data)" != "$(id -u pomona)" ]; then
    chown -R pomona:pomona /data
  fi
  exec setpriv --reuid=pomona --regid=pomona --init-groups env HOME=/home/pomona pomona "$@"
fi
exec pomona "$@"
