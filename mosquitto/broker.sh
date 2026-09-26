#!/bin/sh
# Starts Mosquitto with credentials generated on first start, then regenerates its
# password file and ACL and reloads them (SIGHUP) whenever a producer credential changes.
# Runs as root; Mosquitto drops to its own user. Central runs as uid 65532.
set -eu
secrets=/secrets
auth=/mosquitto/auth
central_uid=65532
umask 077

random_secret() { od -An -tx1 -N32 /dev/urandom | tr -d ' \n'; }
ensure() {
  [ -s "$secrets/$1" ] && return
  random_secret > "$secrets/$1.tmp"
  mv "$secrets/$1.tmp" "$secrets/$1"
}
# Producer names are MQTT users, source IDs and topic levels; ':' would break the password file.
valid_producer() {
  case "$1" in
    '' | [!A-Za-z0-9]* | *[!A-Za-z0-9._-]* | central | homeassistant | demo-source) return 1 ;;
  esac
  [ "${#1}" -le 64 ]
}
generate() {
  mkdir -p "$auth"
  cp /cajui/acl "$auth/acl.tmp"
  : > "$auth/passwords.tmp"
  for name in central homeassistant demo-source; do
    printf '%s:%s\n' "$name" "$(cat "$secrets/$name")" >> "$auth/passwords.tmp"
  done
  for path in "$secrets"/producers/*; do
    [ -f "$path" ] || continue
    name=$(basename "$path")
    if ! valid_producer "$name"; then
      echo "Ignoring invalid producer credential name: $name" >&2
      continue
    fi
    printf '%s:%s\n' "$name" "$(cat "$path")" >> "$auth/passwords.tmp"
    # Telemetry plus the management channel of cajui-firmware (docs/management-v1.md).
    {
      printf '\nuser %s\ntopic write telemetry/v1/%s/+/samples\n' "$name" "$name"
      printf 'topic write manage/v1/%s/+/availability\n' "$name"
      printf 'topic write manage/v1/%s/+/state\n' "$name"
      printf 'topic write manage/v1/%s/+/results\n' "$name"
      printf 'topic read manage/v1/%s/+/commands\n' "$name"
      # Home Assistant Discovery, confined to the producer's node level (docs/home-assistant.md).
      printf 'topic write homeassistant/sensor/%s/+/config\n' "$name"
    } >> "$auth/acl.tmp"
  done
  mosquitto_passwd -U "$auth/passwords.tmp"
  chown -R mosquitto:mosquitto "$auth"
  mv "$auth/passwords.tmp" "$auth/passwords"
  mv "$auth/acl.tmp" "$auth/acl"
}
fingerprint() {
  find "$secrets/producers" -type f | sort | xargs -r md5sum | md5sum
}

mkdir -p "$secrets/producers"
for name in api-token central homeassistant demo-source; do ensure "$name"; done
# Central reads only its API token and broker password.
chown "$central_uid" "$secrets/api-token" "$secrets/central"
chmod 0400 "$secrets/api-token" "$secrets/central"
generate
chown -R mosquitto:mosquitto /mosquitto/data

mosquitto -c /cajui/mosquitto.conf &
pid=$!
trap 'kill -TERM "$pid" 2>/dev/null; wait "$pid"; exit 0' TERM INT
last=$(fingerprint)
while kill -0 "$pid" 2>/dev/null; do
  sleep 2 &
  wait $! || true
  current=$(fingerprint)
  if [ "$current" != "$last" ]; then
    generate
    kill -HUP "$pid"
    last=$current
    echo "Producer credentials reloaded"
  fi
done
wait "$pid"
