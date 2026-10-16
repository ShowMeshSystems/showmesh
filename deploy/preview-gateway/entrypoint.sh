#!/bin/sh
# The NDI runtime finds sources through avahi-daemon over D-Bus, so the image
# carries its own copy of both. They run as root and the gateway does not.
set -u

mkdir -p /run/dbus
rm -f /run/dbus/pid /run/avahi-daemon/pid
if dbus-daemon --system --fork && avahi-daemon -D --no-rlimits -f /etc/showmesh/avahi-daemon.conf; then
	:
else
	echo "entrypoint: could not start avahi-daemon; NDI sources will not be found." >&2
fi

# A registry left by an earlier run can remember a failed NDI plugin load.
rm -f "${GST_REGISTRY:-/tmp/gst-registry.bin}"

# libndi is looked up in /opt/ndi only by the unprivileged process.
exec setpriv --reuid=10001 --regid=10001 --clear-groups \
	--no-new-privs --inh-caps=-all --bounding-set=-all \
	env HOME=/home/showmesh LD_LIBRARY_PATH=/opt/ndi "$@"
