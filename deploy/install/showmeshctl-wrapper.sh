#!/bin/sh
# Runs showmeshctl with this coordinator's address and administrator token unless the caller set their own.
if [ -z "${SHOWMESH_CTL_TOKEN:-}" ] && [ -r /etc/showmesh/showmeshctl.env ]; then
  # shellcheck disable=SC1091
  . /etc/showmesh/showmeshctl.env
  export SHOWMESH_SERVER SHOWMESH_CTL_TOKEN
fi
exec /usr/local/lib/showmesh/showmeshctl "$@"
