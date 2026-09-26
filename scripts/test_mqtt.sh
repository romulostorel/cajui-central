#!/bin/sh
# Run from the repository root. Isolated project; only its containers and volumes are removed.
set -eu
export CAJUI_MQTT_PORT="${CAJUI_TEST_MQTT_PORT:-18883}" CAJUI_MQTT_BIND=127.0.0.1
project="cajui-mqtt-integration-$$"
compose() { docker compose -p "$project" -f compose.yaml -f compose.dev.yaml "$@"; }
cleanup() { compose down -v >/dev/null 2>&1; }
trap cleanup EXIT
compose up -d --build --wait broker
secrets="${project}_secrets"
# A throwaway client with read access to the generated credentials.
client() {
  docker run --rm --network "${project}_default" -v "$secrets":/secrets:ro \
    --entrypoint sh eclipse-mosquitto:2.0.22 -c "$1" 2>&1 || true
}
publish_as() { # user, password file, topic
  client "mosquitto_pub -h broker -V mqttv5 -q 1 -u $1 -P \"\$(cat /secrets/$2)\" -t $3 -m probe"
}
# Waits for the broker's credential reload (it polls every two seconds).
eventually() { # expected text, command...
  expected=$1; shift
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    "$@" | grep -q "$expected" && return 0
    sleep 1
  done
  return 1
}
docker run --rm --network "${project}_default" \
  -e CAJUI_TEST_MQTT_URL=tcp://broker:1883 -e CAJUI_TEST_SECRETS=/secrets \
  -v "$secrets":/secrets:ro -v "$PWD":/src -w /src \
  -v cajui-go-mod:/go/pkg/mod -v cajui-go-build:/root/.cache/go-build \
  golang:1.27 make check
# MQTT 3.1.1 PUBACK has no negative reason code. MQTT 5 makes ACL rejection observable.
publish_as demo-source demo-source telemetry/v1/other/device/samples | grep -q 'Not authorized' \
  || { echo 'ACL rejection was not observed'; exit 1; }
# A producer created at runtime may publish only under its own namespace.
producer=integration-producer
compose run --rm credentials producer "$producer" > /dev/null
allowed() {
  out=$(publish_as "$producer" "producers/$producer" "telemetry/v1/$producer/device/samples")
  printf '%s\n' "$out" | grep -q 'Not authorized' && echo denied || echo allowed
}
eventually allowed allowed || { echo 'New producer was not accepted after reload'; exit 1; }
publish_as "$producer" "producers/$producer" telemetry/v1/demo-source/device/samples | grep -q 'Not authorized' \
  || { echo 'Producer ACL rejection was not observed'; exit 1; }
# The producer also owns its management topics (cajui-firmware docs/management-v1.md), and
# only those; read-only accounts cannot publish state.
publish_as "$producer" "producers/$producer" "manage/v1/$producer/device/state" | grep -q 'Not authorized' \
  && { echo 'Producer management state was rejected'; exit 1; }
publish_as "$producer" "producers/$producer" manage/v1/demo-source/device/state | grep -q 'Not authorized' \
  || { echo 'Foreign management state was accepted'; exit 1; }
publish_as "$producer" "producers/$producer" "manage/v1/$producer/device/commands" | grep -q 'Not authorized' \
  || { echo 'Producer could publish its own commands'; exit 1; }
publish_as central central "manage/v1/$producer/device/state" | grep -q 'Not authorized' \
  || { echo 'Central could publish management state'; exit 1; }
# Only Central sends commands; the read-only account cannot.
publish_as central central "manage/v1/$producer/device/commands" | grep -q 'Not authorized' \
  && { echo 'Central could not publish a command'; exit 1; }
publish_as homeassistant homeassistant "manage/v1/$producer/device/commands" | grep -q 'Not authorized' \
  || { echo 'Read-only account could publish a command'; exit 1; }
publish_as "$producer" "producers/$producer" "manage/v1/$producer/device/results" | grep -q 'Not authorized' \
  && { echo 'Producer could not publish a result'; exit 1; }
# Home Assistant Discovery: a producer writes configurations only under its own node level.
publish_as "$producer" "producers/$producer" "homeassistant/sensor/$producer/device_temperature/config" | grep -q 'Not authorized' \
  && { echo 'Producer could not publish its discovery configuration'; exit 1; }
publish_as "$producer" "producers/$producer" homeassistant/sensor/demo-source/device_temperature/config | grep -q 'Not authorized' \
  || { echo 'Producer could publish another source discovery configuration'; exit 1; }
publish_as "$producer" "producers/$producer" "homeassistant/switch/$producer/device_relay/config" | grep -q 'Not authorized' \
  || { echo 'Producer could publish a non-sensor discovery configuration'; exit 1; }
# Imported and removed credentials take effect without restarting the broker.
printf 'imported-secret-123\n' | compose run --rm -T credentials import imported-producer > /dev/null
imported() {
  client 'mosquitto_pub -h broker -V mqttv5 -q 1 -u imported-producer -P imported-secret-123 -t telemetry/v1/imported-producer/d/samples -m probe' \
    | grep -q 'Not authorized' && echo denied || echo allowed
}
eventually allowed imported || { echo 'Imported producer was not accepted'; exit 1; }
compose run --rm credentials remove imported-producer > /dev/null
eventually denied imported || { echo 'Removed producer was still accepted'; exit 1; }
compose run --rm credentials producer 'bad name' > /dev/null 2>&1 && { echo 'Invalid name accepted'; exit 1; }
printf '%s\n' 'Broker integration, generated credentials, ACL and reload checks passed.'
