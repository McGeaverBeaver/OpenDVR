#!/bin/sh
# OpenDVR container entrypoint.
#
# The image starts as root only to set things up, then drops to the
# unprivileged account the files should belong to. PUID and PGID follow
# the usual Unraid convention (99:100 is nobody:users). The account is
# also put in the groups that own /dev/dri so Quick Sync keeps working.
#
# Set PUID=0 to keep running as root (not recommended), or start the
# container with --user to skip this script's setup entirely.
set -e

PUID="${PUID:-99}"
PGID="${PGID:-100}"
UMASK="${UMASK:-022}"

if [ "$(id -u)" != "0" ] || [ "$PUID" = "0" ]; then
    umask "$UMASK"
    exec /app/opendvr "$@"
fi

# An account with the requested ids (names do not matter, ids do).
if ! getent group "$PGID" >/dev/null; then
    groupadd -g "$PGID" opendvr
fi
if ! getent passwd "$PUID" >/dev/null; then
    useradd -u "$PUID" -g "$PGID" -d /config -M -s /usr/sbin/nologin opendvr
fi
user="$(getent passwd "$PUID" | cut -d: -f1)"

# Groups that own the render nodes, so hardware encoding works unprivileged.
extra=""
for node in /dev/dri/*; do
    [ -e "$node" ] || continue
    gid="$(stat -c %g "$node")"
    getent group "$gid" >/dev/null || groupadd -g "$gid" "render$gid"
    extra="$extra,$gid"
done
extra="${extra#,}"

# Make the mounted folders writable. Config and the live buffer are small
# and get fixed fully; the recordings share may be huge, so only its top
# levels are touched (that is where the server writes).
for d in "${DATA_DIR:-/config}" "${BUFFER_DIR:-/buffer}"; do
    mkdir -p "$d"
    chown -R "$PUID:$PGID" "$d" 2>/dev/null || echo "warning: could not chown $d" >&2
done
rec="${RECORDINGS_DIR:-/recordings}"
mkdir -p "$rec"
find "$rec" -maxdepth 2 ! -user "$PUID" -exec chown "$PUID:$PGID" {} + 2>/dev/null || echo "warning: could not chown $rec" >&2

umask "$UMASK"
echo "opendvr: running as uid=$PUID gid=$PGID groups=$extra umask=$UMASK"
if [ -n "$extra" ]; then
    exec setpriv --reuid="$PUID" --regid="$PGID" --groups="$extra" --inh-caps=-all /app/opendvr "$@"
fi
exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups --inh-caps=-all /app/opendvr "$@"
