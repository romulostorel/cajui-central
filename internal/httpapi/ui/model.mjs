import { t, locale, metricLabel } from "./i18n.mjs";
// Presentation helpers. No network, DOM, credentials, or persistent telemetry.
export const states = Object.freeze({
  get ok() {
    return t("states.ok");
  },
  get recorded() {
    return t("states.recorded");
  },
  get stale() {
    return t("states.stale");
  },
  get error() {
    return t("states.error");
  },
  get skipped() {
    return t("states.skipped");
  },
  get empty() {
    return t("states.empty");
  },
  get loading() {
    return t("states.loading");
  },
  get warning() {
    return t("states.warning");
  },
  get info() {
    return t("states.info");
  },
});
export const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export function numeric(value) {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}
export function formatValue(value, digits = 1) {
  return numeric(value) === null
    ? "—"
    : new Intl.NumberFormat(locale(), {
        maximumFractionDigits: digits,
        notation: Math.abs(value) >= 1e7 ? "scientific" : "standard",
      }).format(value);
}
export function formatUnit(unit) {
  const known = { degC: "°C", degF: "°F" };
  return Object.hasOwn(known, unit) ? known[unit] : (unit ?? "");
}
export function label(value) {
  const text = String(value ?? "").replace(/[_-]/g, " ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}
export function age(value, now = Date.now()) {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return t("age.unknown");
  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
  return seconds < 60
    ? t("age.now")
    : seconds < 3600
      ? t("age.minutes", { count: Math.floor(seconds / 60) })
      : seconds < 86400
        ? t("age.hours", { count: Math.floor(seconds / 3600) })
        : t("age.days", { count: Math.floor(seconds / 86400) });
}
export function channelState(reading, at, interval, now = Date.now()) {
  if (!reading) return "empty";
  // Silence and reading failure are independent: preserve failure on the card,
  // and show device silence independently in the device list.
  if (reading.status === "error" || reading.status === "skipped")
    return reading.status;
  if (numeric(reading.value) === null) return "empty";
  if (!interval) return "recorded";
  if (interval > 0 && now - Date.parse(at) >= interval * 3000) return "stale";
  return "ok";
}
export function buildChannels(data, now = Date.now()) {
  const channels = new Map();
  const add = (reading, metadata) => {
    const key = JSON.stringify([
      metadata.transport,
      metadata.source,
      metadata.device,
      reading.sensor_id,
      reading.metric,
      reading.unit,
    ]);
    if (!channels.has(key))
      channels.set(key, {
        key,
        ...metadata,
        sensor: reading.sensor_id,
        metric: reading.metric,
        unit: reading.unit,
        title: metricLabel(reading.metric, label(reading.metric)),
        points: [],
        latestTime: -Infinity,
      });
    const channel = channels.get(key);
    const time = Date.parse(metadata.at);
    if (!Number.isFinite(time)) return;
    channel.points.push({
      time,
      value: reading.status === "ok" ? numeric(reading.value) : null,
    });
    if (time > channel.latestTime) {
      Object.assign(channel, metadata, {
        value: numeric(reading.value),
        state: channelState(reading, metadata.at, metadata.interval, now),
        latestTime: time,
      });
    }
  };
  for (const sample of data.samples ?? [])
    for (const reading of sample.readings ?? [])
      add(reading, {
        transport: "mqtt",
        source: sample.source_id,
        device: sample.device_id,
        at: sample.received_at,
        interval: sample.expected_interval_seconds,
      });
  for (const reading of data.readings ?? [])
    add(
      { ...reading, status: "ok" },
      {
        transport: "http",
        source: "HTTP",
        device: reading.node_id,
        at: reading.received_at,
        interval: 0,
      },
    );
  return [...channels.values()]
    .filter((c) => Number.isFinite(c.latestTime))
    .map((c) => ({ ...c, points: c.points.sort((a, b) => a.time - b.time) }));
}
export function plotGeometry(
  points,
  width = 620,
  height = 170,
  gap = Infinity,
) {
  const valid = points
    .filter((p) => Number.isFinite(p.time))
    .sort((a, b) => a.time - b.time);
  const values = valid.map((p) => numeric(p.value)).filter((v) => v !== null);
  if (!values.length) return null;
  let min = Math.min(...values),
    max = Math.max(...values);
  // Normalize before subtracting so two finite values cannot overflow the range.
  const scale = Math.max(Math.abs(min), Math.abs(max), 1);
  const low = min / scale,
    high = max / scale;
  const pad =
    high === low
      ? Math.max(Math.abs(high) * 0.04, 1 / scale)
      : (high - low) * 0.18;
  const bottom = low - pad,
    top = high + pad;
  min = Math.max(-Number.MAX_VALUE, bottom * scale);
  max = Math.min(Number.MAX_VALUE, top * scale);
  const start = valid[0].time,
    end = valid.at(-1).time;
  const x = (t) => ((t - start) / (end - start || 1)) * width;
  const y = (v) => height - ((v / scale - bottom) / (top - bottom)) * height;
  let path = "",
    open = false,
    previous = null;
  for (const p of valid) {
    if (numeric(p.value) === null) {
      open = false;
      previous = p.time;
      continue;
    }
    if (previous !== null && p.time - previous > gap) open = false;
    path += `${open ? "L" : "M"}${x(p.time).toFixed(2)},${y(p.value).toFixed(2)} `;
    open = true;
    previous = p.time;
  }
  return { path, min, max, start, end, x, y, points: valid };
}
export function csvRows(channels) {
  const cell = (value) => {
    const text = String(value ?? "");
    // Negative numeric measurements stay numeric; untrusted text cannot start
    // a spreadsheet formula, including after leading whitespace/control bytes.
    const safe =
      typeof value === "number"
        ? text
        : text.replace(/^(?=\s*[=+@-]|[\t\r\n])/, "'");
    return '"' + safe.replace(/"/g, '""') + '"';
  };
  const rows = [
    [
      "Source",
      "Device",
      "Sensor",
      "Metric",
      "Value",
      "Unit",
      "Status",
      "Received at",
    ],
  ];
  for (const c of channels)
    rows.push([
      c.source,
      c.device,
      c.sensor,
      c.metric,
      ["ok", "stale", "recorded"].includes(c.state) ? c.value : "",
      c.unit,
      c.state,
      c.at,
    ]);
  return rows.map((row) => row.map(cell).join(",")).join("\r\n");
}
export function contrast(foreground, background) {
  const luminance = (hex) => {
    const rgb = hex
      .replace("#", "")
      .match(/../g)
      .map((c) => parseInt(c, 16) / 255)
      .map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
    return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
  };
  const a = luminance(foreground),
    b = luminance(background);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

// Version 1 producers can report these exact radio channels as link diagnostics.
// Do not infer diagnostics from a unit alone: an arbitrary sensor may measure dB.
export function isLinkDiagnostic(channel) {
  return (
    channel.sensor === "radio" &&
    ((channel.metric === "rssi" && channel.unit === "dBm") ||
      (channel.metric === "snr" && channel.unit === "dB"))
  );
}
export function buildDeviceGroups(channels, reportedDevices = []) {
  const groups = new Map();
  const ensure = (transport, source, device) => {
    const key = JSON.stringify([transport, source, device]);
    if (!groups.has(key))
      groups.set(key, {
        key,
        transport,
        source,
        device,
        name: device,
        location: "",
        at: "",
        interval: 0,
        stale: false,
        reportedError: false,
        sensors: [],
        diagnostics: [],
      });
    return groups.get(key);
  };
  for (const d of reportedDevices) {
    const group = ensure("mqtt", d.source_id, d.device_id);
    Object.assign(group, {
      name: d.name ?? d.device_id,
      location: d.location ?? "",
      at: d.last_received_at,
      interval: d.expected_interval_seconds,
      stale: Boolean(d.stale),
      reportedError: Boolean(d.sensor_error),
    });
  }
  for (const c of channels) {
    const group = ensure(c.transport, c.source, c.device);
    if (!group.at || Date.parse(c.at) > Date.parse(group.at)) group.at = c.at;
    if (isLinkDiagnostic(c)) {
      group.diagnostics.push(c);
      continue;
    }
    let sensor = group.sensors.find((s) => s.id === c.sensor);
    if (!sensor) {
      sensor = { id: c.sensor, name: label(c.sensor), channels: [] };
      group.sensors.push(sensor);
    }
    sensor.channels.push(c);
  }
  for (const group of groups.values()) {
    group.sensors.sort((a, b) => a.id.localeCompare(b.id));
    for (const sensor of group.sensors)
      sensor.channels.sort((a, b) => {
        const rank = (c) =>
          c.metric === "temperature" ? 0 : c.metric === "humidity" ? 1 : 2;
        return (
          rank(a) - rank(b) ||
          a.metric.localeCompare(b.metric) ||
          a.unit.localeCompare(b.unit)
        );
      });
    const all = [
      ...group.sensors.flatMap((s) => s.channels),
      ...group.diagnostics,
    ];
    group.error = group.reportedError || all.some((c) => c.state === "error");
    group.attention =
      group.stale ||
      group.error ||
      all.some((c) => ["stale", "skipped", "empty"].includes(c.state));
  }
  return [...groups.values()].sort(
    (a, b) => a.name.localeCompare(b.name) || a.key.localeCompare(b.key),
  );
}

export function measurementLabel(value) {
  return metricLabel(value, label(value));
}

// Management state of cajui-firmware devices (cajui-firmware docs/management-v1.md).
// Missing values stay unknown; nothing here is inferred from a unit or a name.
export function receiverLabel(deviceId) {
  return t("receivers.name", {
    id: String(deviceId ?? "")
      .slice(-4)
      .toUpperCase(),
  });
}
export function deviceStateFor(states, source, device) {
  return (
    (states ?? []).find(
      (s) => s.source_id === source && s.device_id === device,
    ) ?? null
  );
}
export function firmwareText(firmware) {
  const version = firmware?.version;
  if (!version) return t("common.unknown");
  const text = version === "0.0.0" ? t("receivers.local_build") : version;
  return firmware.state === "pending"
    ? t("receivers.firmware_pending", { version: text })
    : text;
}
export function uptimeText(seconds) {
  if (numeric(seconds) === null) return t("common.unknown");
  return seconds < 3600
    ? t("receivers.duration.minutes", { count: Math.floor(seconds / 60) })
    : seconds < 86400
      ? t("receivers.duration.hours", { count: Math.floor(seconds / 3600) })
      : t("receivers.duration.days", { count: Math.floor(seconds / 86400) });
}
// Status plus the notices an operator can act on, most urgent first.
export function receiverSummary(state) {
  const online = state.availability === "online";
  const offline = state.availability === "offline";
  const notices = [];
  if (offline)
    notices.push({ level: "error", text: t("receivers.offline_notice") });
  const depth = numeric(state.queue?.depth);
  if (online && depth)
    notices.push({
      level: "warning",
      text: t("receivers.queue_notice", { count: depth }),
    });
  if (state.pairing?.open)
    notices.push({
      level: "info",
      text: t("receivers.pairing_notice", {
        count: state.pairing.requests?.length ?? 0,
      }),
    });
  if (state.firmware?.state === "pending")
    notices.push({ level: "info", text: t("receivers.pending_notice") });
  if (state.retained)
    notices.push({ level: "info", text: t("receivers.retained_notice") });
  return {
    status: online ? "online" : offline ? "offline" : "unknown",
    notices,
  };
}
export function linkText(frame) {
  if (!frame) return t("common.unknown");
  const rssi = numeric(frame.rssi_dbm),
    snr = numeric(frame.snr_db);
  if (rssi === null && snr === null) return t("common.unknown");
  return [
    rssi === null ? null : `${formatValue(rssi, 0)} dBm`,
    snr === null ? null : `${formatValue(snr, 1)} dB`,
  ]
    .filter(Boolean)
    .join(" · ");
}
