import test from "node:test";
import assert from "node:assert/strict";
import {
  escapeHTML,
  numeric,
  formatValue,
  formatUnit,
  age,
  channelState,
  buildChannels,
  plotGeometry,
  csvRows,
  contrast,
  buildDeviceGroups,
  isLinkDiagnostic,
  receiverLabel,
  deviceStateFor,
  firmwareText,
  uptimeText,
  receiverSummary,
  linkText,
} from "../../internal/httpapi/ui/model.mjs";
import { setLocale } from "../../internal/httpapi/ui/i18n.mjs";
import { demoData } from "../../docs/brand/demo.mjs";

test("missing, zero and failures remain distinct", () => {
  assert.equal(numeric(null), null);
  assert.equal(numeric(""), null);
  assert.equal(numeric(NaN), null);
  assert.equal(numeric(Infinity), null);
  assert.equal(formatValue(null), "—");
  assert.equal(formatValue(0), "0");
  assert.equal(formatUnit("degC"), "°C");
  const at = "2026-01-01T00:00:00Z",
    now = Date.parse(at) + 900000;
  assert.equal(channelState({ status: "ok", value: 0 }, at, 300, now), "stale");
  assert.equal(
    channelState({ status: "ok", value: 0 }, at, 300, now - 1),
    "ok",
  );
  assert.equal(
    channelState({ status: "error", value: 0 }, at, 300, now),
    "error",
  );
  assert.equal(channelState({ status: "skipped" }, at, 300, now), "skipped");
  assert.equal(
    channelState({ status: "ok", value: null }, at, 300, now),
    "empty",
  );
  assert.equal(channelState(null, at, 300, now), "empty");
  assert.equal(age("invalid", now), "Time unknown");
  assert.equal(age(at, now), "15 min ago");
});
test("channel identity includes source, device, sensor, metric and unit", () => {
  const data = demoData(Date.parse("2026-01-01T12:00:00Z"));
  const result = buildChannels(data, Date.parse(data.generated_at));
  assert.equal(result.length, 4);
  assert.equal(result[0].value, 24.6);
  assert.equal(result[3].state, "error");
  assert.equal(result[0].points.length, 49);
  const duplicate = {
    ...data.samples.at(-3),
    source_id: "another",
    received_at: data.generated_at,
  };
  data.samples.push(duplicate);
  assert.equal(buildChannels(data).length, 6);
  const base = {
    node_id: "node",
    sensor_id: "s",
    metric: "temperature",
    unit: "degC",
    value: 0,
    received_at: data.generated_at,
  };
  const http = buildChannels({ readings: [base, { ...base, unit: "degF" }] });
  assert.equal(http.length, 2);
  assert.equal(http[0].value, 0);
});
test("charts preserve gaps, actual time spacing, negative and constant readings", () => {
  const plot = plotGeometry(
    [
      { time: 0, value: 0 },
      { time: 10, value: null },
      { time: 20, value: -5 },
      { time: 100, value: 10 },
    ],
    100,
    100,
    50,
  );
  assert.equal((plot.path.match(/M/g) || []).length, 3);
  assert.equal(plot.x(20), 20);
  assert.ok(plot.min < -5);
  assert.ok(plot.max > 10);
  assert.equal(plotGeometry([{ time: 1, value: null }]), null);
  const constant = plotGeometry([
    { time: 1, value: 0 },
    { time: 2, value: 0 },
  ]);
  assert.ok(Number.isFinite(constant.y(0)));
  assert.ok(!constant.path.includes("NaN"));
});
test("escaping and CSV export do not turn names into markup or formulas", () => {
  assert.equal(escapeHTML('<img a="x">'), "&lt;img a=&quot;x&quot;&gt;");
  const csv = csvRows([
    {
      source: "=SUM(1)",
      device: "<script>",
      sensor: 'a"b',
      metric: "temperature",
      value: 0,
      unit: "degC",
      state: "ok",
      at: "time",
    },
    { state: "error", value: 0 },
  ]);
  assert.ok(csv.includes('"\'=SUM(1)"'));
  assert.ok(csv.includes('"a""b"'));
  assert.ok(csv.includes('"0"'));
  assert.ok(!csv.split("\r\n")[2].includes('"0"'));
});
test("foundational text pairs meet WCAG AA contrast", () => {
  for (const pair of [
    ["#20342e", "#ffffff"],
    ["#58685f", "#ffffff"],
    ["#ffffff", "#265d48"],
    ["#825513", "#fff0d5"],
    ["#a23838", "#fbe8e5"],
    ["#ecf3e9", "#1c2922"],
    ["#b4c2b6", "#1c2922"],
  ])
    assert.ok(contrast(...pair) >= 4.5, pair.join(" on "));
});

test("valid extreme values and prototype-like units stay representable", () => {
  assert.equal(formatUnit("constructor"), "constructor");
  const g = plotGeometry([
    { time: 2, value: Number.MAX_VALUE },
    { time: 1, value: -Number.MAX_VALUE },
  ]);
  assert.ok(Number.isFinite(g.min) && Number.isFinite(g.max));
  assert.ok(!/NaN|Infinity/.test(g.path));
  assert.ok(Number.isFinite(g.y(0)));
  assert.ok(formatValue(Number.MAX_VALUE).length < 20);
  const r = {
    sensor_id: "s",
    metric: "temperature",
    unit: "degC",
    value: 1,
    status: "ok",
  };
  const state = {
    samples: [
      {
        source_id: "HTTP",
        device_id: "d",
        received_at: "2026-01-01T00:00:00Z",
        expected_interval_seconds: 1,
        readings: [r],
      },
    ],
    readings: [{ ...r, node_id: "d", received_at: "2026-01-01T00:00:00Z" }],
  };
  assert.equal(buildChannels(state).length, 2);
});

test("CSV keeps negative measurements numeric and escapes whitespace formulas", () => {
  const csv = csvRows([
    { source: "  =SUM(1)", device: "\n=1", value: -5, state: "ok" },
  ]);
  assert.ok(csv.includes('"-5"'));
  assert.ok(csv.includes('"\'  =SUM(1)"'));
  assert.ok(csv.includes('"\'\n=1"'));
});

test("device groups keep physical sensors together and isolate radio diagnostics", () => {
  const now = Date.parse("2026-01-01T00:00:00Z");
  const sample = {
    source_id: "source-a",
    device_id: "node-1",
    received_at: new Date(now).toISOString(),
    expected_interval_seconds: 300,
    readings: [
      {
        sensor_id: "sensor-1",
        metric: "temperature",
        unit: "degC",
        value: 24,
        status: "ok",
      },
      {
        sensor_id: "sensor-1",
        metric: "humidity",
        unit: "%",
        value: 0,
        status: "ok",
      },
      {
        sensor_id: "radio",
        metric: "rssi",
        unit: "dBm",
        value: -85,
        status: "ok",
      },
      {
        sensor_id: "radio",
        metric: "snr",
        unit: "dB",
        value: 12,
        status: "ok",
      },
    ],
  };
  const channels = buildChannels({ samples: [sample] }, now);
  const [group] = buildDeviceGroups(channels, [
    {
      source_id: "source-a",
      device_id: "node-1",
      last_received_at: sample.received_at,
      expected_interval_seconds: 300,
    },
  ]);
  assert.equal(group.sensors.length, 1);
  assert.equal(group.sensors[0].channels.length, 2);
  assert.equal(group.sensors[0].channels[0].metric, "temperature");
  assert.equal(group.sensors[0].channels[1].value, 0);
  assert.equal(group.diagnostics.length, 2);
  assert.equal(group.attention, false);
  assert.equal(
    isLinkDiagnostic({ sensor: "noise", metric: "rssi", unit: "dBm" }),
    false,
  );
  assert.equal(
    isLinkDiagnostic({ sensor: "radio", metric: "rssi", unit: "V" }),
    false,
  );
  const independent = buildChannels(
    {
      samples: [sample, { ...sample, source_id: "source-b" }],
      readings: [
        {
          node_id: "node-1",
          sensor_id: "sensor-1",
          metric: "temperature",
          unit: "degC",
          value: 0,
          received_at: sample.received_at,
        },
      ],
    },
    now,
  );
  assert.equal(buildDeviceGroups(independent).length, 3);
});

test("grouped views preserve units, unknown metrics, errors and silent devices", () => {
  const base = {
    transport: "mqtt",
    source: "s",
    device: "d",
    sensor: "sensor-1",
    metric: "temperature",
    unit: "degC",
    at: "2026-01-01T00:00:00Z",
    state: "ok",
    value: 0,
  };
  const groups = buildDeviceGroups(
    [
      base,
      { ...base, unit: "degF" },
      {
        ...base,
        sensor: "sensor-2",
        metric: "custom",
        state: "error",
        value: null,
      },
    ],
    [
      { source_id: "s", device_id: "d", stale: true },
      { source_id: "s", device_id: "silent", stale: true },
    ],
  );
  assert.equal(groups.length, 2);
  assert.equal(groups[0].sensors.length, 2);
  assert.equal(groups[0].sensors[0].channels.length, 2);
  assert.equal(groups[0].error, true);
  assert.equal(groups[0].stale, true);
  assert.equal(groups[1].sensors.length, 0);
  assert.equal(groups[1].attention, true);
});

import {
  workspaceGroups,
  registeredGroups,
  automaticSections,
  itemChoices,
  resolveItem,
  environmentalSensors,
} from "../../internal/httpapi/ui/workspace-model.mjs";
function inventoryFixture() {
  const at = "2026-01-01T00:00:00Z";
  return {
    generated_at: "2026-01-01T00:20:00Z",
    samples: [],
    readings: [],
    devices: [],
    workspace: {
      devices: [
        {
          id: 1,
          transport: "mqtt",
          source: "a",
          device: "node",
          name: "North",
          location: "Field",
          received_at: at,
          interval: 300,
        },
        {
          id: 2,
          transport: "mqtt",
          source: "b",
          device: "node",
          name: "South",
          location: "",
          received_at: at,
          interval: 300,
        },
        {
          id: 3,
          transport: "http",
          source: "HTTP",
          device: "node",
          name: "",
          location: "",
          received_at: at,
          interval: 0,
        },
      ],
      sensors: [1, 2, 3].map((i) => ({
        id: i,
        device_id: i,
        sensor: "ambient",
        name: i < 3 ? "Climate" : "",
        location: "",
        measurements: [
          {
            metric: "temperature",
            unit: "degC",
            value: 0,
            status: "ok",
            received_at: at,
            interval: i < 3 ? 300 : 0,
          },
          {
            metric: "humidity",
            unit: "%",
            value: null,
            status: "error",
            received_at: at,
            interval: i < 3 ? 300 : 0,
          },
        ],
      })),
      layout: { revision: 0, sections: null },
    },
  };
}
test("inventory survives absent recent readings and keeps source-scoped identities", () => {
  const state = inventoryFixture(),
    groups = workspaceGroups(state);
  assert.equal(groups.length, 3);
  const north = groups.find((g) => g.registryID === 1);
  assert.equal(north.name, "North");
  assert.equal(north.stale, true);
  assert.equal(north.sensors[0].channels[0].value, 0);
  assert.equal(north.sensors[0].channels[0].state, "stale");
  assert.equal(north.sensors[0].channels[1].state, "error");
  assert.equal(north.sensors[0].channels[0].sensorName, "Climate");
  assert.equal(registeredGroups(groups).length, 2);
  assert.notEqual(
    north.sensors[0].channels[0].key,
    groups.find((g) => g.registryID === 2).sensors[0].channels[0].key,
  );
  assert.equal(
    groups.find((g) => g.registryID === 3).sensors[0].channels[0].state,
    "recorded",
  );
});
test("inventory latest value merges without duplicating a history point", () => {
  const state = inventoryFixture(),
    m = state.workspace.sensors[0].measurements[0];
  state.samples = [
    {
      source_id: "a",
      device_id: "node",
      received_at: m.received_at,
      expected_interval_seconds: 300,
      readings: [{ sensor_id: "ambient", ...m }],
    },
  ];
  let c = workspaceGroups(state).find((g) => g.registryID === 1).sensors[0]
    .channels[0];
  assert.equal(c.points.length, 1);
  state.workspace.sensors[0].measurements[0] = {
    ...m,
    value: 12,
    received_at: "2026-01-01T00:05:00Z",
  };
  c = workspaceGroups(state).find((g) => g.registryID === 1).sensors[0]
    .channels[0];
  assert.equal(c.points.length, 2);
  assert.equal(c.value, 12);
});
test("layout references registrations and excludes diagnostics", () => {
  const state = inventoryFixture(),
    catalog = state.workspace;
  catalog.sensors.push({
    id: 4,
    device_id: 1,
    sensor: "radio",
    name: "",
    measurements: [
      {
        metric: "rssi",
        unit: "dBm",
        value: -70,
        status: "ok",
        received_at: catalog.devices[0].received_at,
        interval: 300,
      },
    ],
  });
  assert.equal(environmentalSensors(catalog).length, 3);
  const sections = automaticSections(catalog);
  assert.equal(sections.length, 2);
  assert.equal(sections[0].items.length, 2);
  assert.equal(sections[1].items.length, 2);
  assert.equal(itemChoices(catalog).length, 8);
  const groups = workspaceGroups(state);
  assert.equal(
    resolveItem({ kind: "sensor", sensor_id: 1 }, groups).channels.length,
    2,
  );
  assert.equal(
    resolveItem(
      {
        kind: "measurement",
        sensor_id: 1,
        metric: "temperature",
        unit: "degC",
      },
      groups,
    ).channels.length,
    1,
  );
  assert.equal(
    resolveItem(
      { kind: "measurement", sensor_id: 1, metric: "temperature", unit: "K" },
      groups,
    ),
    null,
  );
  assert.equal(resolveItem({ kind: "sensor", sensor_id: 3 }, groups), null);
  assert.equal(resolveItem({ kind: "device", device_id: 99 }, groups), null);
  assert.equal(
    resolveItem({ kind: "device", device_id: 2 }, groups).group.name,
    "South",
  );
  catalog.sensors[0].name = "Renamed";
  assert.equal(
    resolveItem({ kind: "sensor", sensor_id: 1 }, workspaceGroups(state)).sensor
      .name,
    "Renamed",
  );
  assert.equal(catalog.layout.sections, null);
});

test("receiver state keeps unknown values unknown and names actionable notices", () => {
  setLocale("en-US");
  assert.equal(receiverLabel("000048ca433c5e10"), "Receiver 5E10");
  const states = [
    { source_id: "r", device_id: "a", role: "receiver" },
    { source_id: "r", device_id: "b", role: "transmitter" },
  ];
  assert.equal(deviceStateFor(states, "r", "b").role, "transmitter");
  assert.equal(deviceStateFor(states, "other", "b"), null);
  assert.equal(deviceStateFor(undefined, "r", "b"), null);
  assert.equal(firmwareText(undefined), "Unknown");
  assert.equal(firmwareText({ version: "0.2.0", state: "valid" }), "0.2.0");
  assert.equal(
    firmwareText({ version: "0.0.0", state: "flashed" }),
    "Local build",
  );
  assert.equal(
    firmwareText({ version: "0.3.0", state: "pending" }),
    "0.3.0, awaiting confirmation",
  );
  assert.equal(uptimeText(undefined), "Unknown");
  assert.equal(uptimeText(0), "0 min");
  assert.equal(uptimeText(7200), "2 h");
  assert.equal(uptimeText(172800), "2 d");
  assert.deepEqual(receiverSummary({}), { status: "unknown", notices: [] });
  assert.deepEqual(
    receiverSummary({ availability: "online", queue: { depth: 0 } }).notices,
    [],
  );
  const busy = receiverSummary({
    availability: "online",
    queue: { depth: 3 },
    pairing: { open: true, requests: [{}] },
    firmware: { state: "pending" },
    retained: true,
  });
  assert.equal(busy.status, "online");
  assert.deepEqual(
    busy.notices.map((n) => n.level),
    ["warning", "info", "info", "info"],
  );
  assert.match(busy.notices[0].text, /3 readings are waiting/);
  const offline = receiverSummary({
    availability: "offline",
    queue: { depth: 3 },
  });
  assert.equal(offline.status, "offline");
  assert.deepEqual(
    offline.notices.map((n) => n.level),
    ["error"],
  );
  assert.equal(linkText(null), "Unknown");
  assert.equal(linkText({ rssi_dbm: null }), "Unknown");
  assert.equal(linkText({ rssi_dbm: -82, snr_db: 9.5 }), "-82 dBm · 9.5 dB");
  assert.equal(linkText({ snr_db: -2 }), "-2 dB");
  setLocale("pt-BR");
  assert.equal(receiverLabel("000048ca433c5e10"), "Receptor 5E10");
  assert.match(
    receiverSummary({ availability: "offline" }).notices[0].text,
    /Wi-Fi do receptor/,
  );
  setLocale("en-US");
});
