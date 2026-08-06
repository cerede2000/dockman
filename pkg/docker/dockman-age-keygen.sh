#!/bin/sh

set -eu

identity_path="${1:-/config/secrets/dockman-sops-age-key.txt}"

case "${identity_path}" in
  /config/*) ;;
  *)
    echo "The age identity must be stored below the persistent /config directory." >&2
    exit 2
    ;;
esac

if [ -e "${identity_path}" ]; then
  echo "Refusing to overwrite the existing age identity: ${identity_path}" >&2
  echo "Its public recipient is:" >&2
  exec age-keygen -y "${identity_path}"
fi

runtime_uid="${PUID:-0}"
runtime_gid="${PGID:-0}"
case "${runtime_uid}" in
  ''|*[!0-9]*)
    echo "PUID and PGID must be numeric to generate an age identity." >&2
    exit 2
    ;;
esac
case "${runtime_gid}" in
  ''|*[!0-9]*)
    echo "PUID and PGID must be numeric to generate an age identity." >&2
    exit 2
    ;;
esac

identity_dir="$(dirname "${identity_path}")"
umask 077
mkdir -p "${identity_dir}"
chmod 0700 "${identity_dir}"
age-keygen -o "${identity_path}"
chmod 0600 "${identity_path}"

if [ "$(id -u)" = "0" ]; then
  chown "${runtime_uid}:${runtime_gid}" "${identity_dir}" "${identity_path}"
fi

echo "Age identity created at ${identity_path}. Back it up outside this host." >&2
echo "Public recipient:" >&2
exec age-keygen -y "${identity_path}"
