#!/bin/sh
set -e

echo "Dockman entrypoint"

# Ensure we are in the /app folder
cd /app

PUID=${PUID:-1000}
PGID=${PGID:-1000}

# Docker Compose/BuildKit writes CLI state even for a dry-run. Keep that state
# in Dockman's writable configuration volume instead of the read-only rootfs.
# An explicit DOCKER_CONFIG remains supported for advanced deployments.
DOCKER_CONFIG=${DOCKER_CONFIG:-${DOCKMAN_CONFIG:-/config}/docker-cli}
export DOCKER_CONFIG

# Does this directory accept a write? `test -w` answers yes to root on a
# read-only filesystem - root passes the permission check, and only the write
# itself returns EROFS - so the write is what decides.
can_write() {
    probe="$1/.dockman-write-probe.$$"
    if touch "$probe" 2>/dev/null; then
        rm -f "$probe"
        return 0
    fi
    return 1
}

# Compose, Buildx and the Docker CLI all write temporary files, and a
# read-only rootfs takes /tmp with it. Point them at the configuration volume
# rather than making a tmpfs mount a requirement; Go, Compose and Buildx all
# honour TMPDIR, and Dockman passes it on to the builds it runs here. Root
# needs it as much as anyone: it is the rootfs, not the user, that is
# read-only.
if ! can_write /tmp; then
    TMPDIR=${TMPDIR:-${DOCKMAN_CONFIG:-/config}/tmp}
    export TMPDIR
    mkdir -p "$TMPDIR"
    echo "Read-only root filesystem: temporary files go to $TMPDIR"
fi

# If Root (PUID 0)
if [ "$PUID" -eq 0 ]; then
    echo "Running as root..."
    # no perm changed needed
    # Run the app directly
    exec "$@"
fi

# A read-only root filesystem - the hardening switch of NAS appliances, and
# `read_only: true` in Compose - cannot take a new account: `addgroup` writes
# /etc/group and the container never started. The account only ever existed to
# drop privileges, which su-exec does from plain ids just as well, so on a
# read-only rootfs every step that writes to it is skipped instead.
if can_write /etc; then
    WRITABLE_ROOTFS=1
else
    WRITABLE_ROOTFS=0
    echo "Read-only root filesystem: running as $PUID:$PGID without creating an account"
fi

if [ "$WRITABLE_ROOTFS" -eq 1 ]; then
    # Create the Group
    if ! getent group appgroup >/dev/null; then
        addgroup -g "$PGID" appgroup
    fi

    # Create the User
    if ! getent passwd appuser >/dev/null; then
        adduser -u "$PUID" -G appgroup -h /app -D appuser
    fi
fi

# Docker Socket Permissions
# the mounted socket, or the one DOCKER_HOST names
DOCKER_SOCKET=/var/run/docker.sock
case "${DOCKER_HOST:-}" in
    unix://*) DOCKER_SOCKET=${DOCKER_HOST#unix://} ;;
esac

SOCKET_GROUP_GRANTED=0
# if the socket exists
if [ -S "$DOCKER_SOCKET" ]; then
    # Get the group ID of the host's docker socket
    DOCKER_GID=$(stat -c '%g' "$DOCKER_SOCKET")

    if [ "$WRITABLE_ROOTFS" -eq 1 ]; then
        # Check if a group with that GID already exists in the container
        DOCKER_GROUP=$(getent group "$DOCKER_GID" | cut -d: -f1)

        if [ -z "$DOCKER_GROUP" ]; then
            # Create the group if it doesn't exist
            addgroup -g "$DOCKER_GID" dockerhost
            DOCKER_GROUP="dockerhost"
        fi

        # Add our appuser to the docker group
        addgroup appuser "$DOCKER_GROUP"
    elif [ "$DOCKER_GID" != "$PGID" ]; then
        # A supplementary group also lives in /etc/group, so this container
        # cannot join it. The daemon can grant it instead - group_add - and
        # that grant is kept below; otherwise say so now rather than leaving a
        # permission denied on the socket to be worked out from the logs.
        if id -G | tr ' ' '\n' | grep -qx "$DOCKER_GID"; then
            SOCKET_GROUP_GRANTED=1
            echo "Read-only root filesystem: keeping the Docker socket group ($DOCKER_GID) granted by the daemon"
        else
            echo "Read-only root filesystem: this container cannot join the Docker socket group ($DOCKER_GID)."
            echo "Grant it from the daemon instead: group_add: [\"$DOCKER_GID\"] in Compose, --group-add $DOCKER_GID with docker run, or PGID=$DOCKER_GID."
        fi
    fi
fi

# Compose, Buildx and the Docker CLI all write temporary files, and a
# read-only rootfs takes /tmp with it. Point them at the configuration volume
# rather than making a tmpfs mount a requirement; Go, Compose and Buildx all
# honour TMPDIR, and Dockman passes it on to the builds it runs here.
OWNED_DIRS="${TMPDIR:-}"

# Without an account there is no home directory either, and the one inherited
# from root is both wrong and read-only.
if [ "$WRITABLE_ROOTFS" -eq 0 ]; then
    HOME=${DOCKMAN_CONFIG:-/config}/home
    export HOME
    OWNED_DIRS="$OWNED_DIRS $HOME"
fi

APP_DIRS="DOCKMAN_COMPOSE_ROOT DOCKMAN_CONFIG DOCKMAN_UI_PATH DOCKER_CONFIG"

# Initialize Directories
# loop through the env variables
for DIR_VAR in $APP_DIRS; do
    # value of the variable name
    DIR_PATH=$(eval echo "\$$DIR_VAR")

    if [ -z "$DIR_PATH" ]; then
        echo "Warning: $DIR_VAR is not set, skipping..."
        continue
    fi

    OWNED_DIRS="$OWNED_DIRS $DIR_PATH"
done

for DIR_PATH in $OWNED_DIRS; do
    echo "Setting dir permissions: $DIR_PATH"
    # The UI directory sits on the rootfs and is only ever read, so a
    # read-only filesystem is fine there; the volumes have to be writable by
    # PUID:PGID, and the server reports it plainly when they are not.
    if ! mkdir -p "$DIR_PATH" 2>/dev/null || ! chown "$PUID:$PGID" "$DIR_PATH" 2>/dev/null; then
        echo "Warning: $DIR_PATH keeps its current owner (read-only filesystem?)"
    fi
done

# Drop permissions and run the application
echo "Launching Dockman as $PUID:$PGID"
if [ "$WRITABLE_ROOTFS" -eq 1 ]; then
    exec su-exec appuser "$@"
fi

# su-exec drops every supplementary group, the one the daemon granted with
# group_add included, which is the only way to reach the Docker socket when
# /etc/group cannot be written. setpriv keeps exactly that one.
if [ "$SOCKET_GROUP_GRANTED" -eq 1 ]; then
    exec setpriv --reuid "$PUID" --regid "$PGID" --groups "$DOCKER_GID" "$@"
fi
exec setpriv --reuid "$PUID" --regid "$PGID" --clear-groups "$@"
