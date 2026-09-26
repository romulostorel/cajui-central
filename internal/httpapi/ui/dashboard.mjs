import { t, locale } from "./i18n.mjs";
import {
  escapeHTML as e,
  isLinkDiagnostic,
  age,
  formatValue,
  formatUnit,
  csvRows,
  states,
  receiverLabel,
  deviceStateFor,
  firmwareText,
  uptimeText,
  receiverSummary,
  linkText,
} from "./model.mjs";
import { icon, metricIcon } from "./icons.mjs";
import {
  workspaceGroups,
  registeredGroups,
  automaticSections,
  resolveItem,
} from "./workspace-model.mjs";
import { openLayoutEditor } from "./layout-editor.mjs";

// A value the firmware reported that this version has no text for.
function known(key, fallback) {
  const text = t(key);
  return text === key ? fallback : text;
}
export function mountDashboard(root, { state = {}, notify }) {
  let snapshot = state,
    channels = [],
    groups = [],
    filter = "all",
    query = "",
    selected = "",
    hours = 24,
    pending = false;
  root.innerHTML = `<header class="page-heading"><div><h1>${t("common.dashboard")}</h1><p>${t("dashboard.description")}</p></div><div class="top-actions"><button class="button" id="organize">${t("dashboard.organize")}</button><button class="button" id="export">${icon("download")}${t("dashboard.export")}</button><button class="button primary" id="refresh">${icon("refresh")}${t("common.refresh")}</button></div></header>
    <div id="fetch-error" class="notice hidden" role="status"></div>
    <section id="summary" class="device-summary" aria-label="${t("dashboard.summary")}"></section>
    <section id="receivers" class="receiver-strip" aria-label="${t("receivers.heading")}" hidden></section>
    <div class="toolbar"><div class="segmented" aria-label="${t("dashboard.filter")}"><button data-filter="all" aria-pressed="true">${t("dashboard.all")}</button><button data-filter="attention" aria-pressed="false">${t("dashboard.attention")}</button></div><label class="search">${icon("search")}<span class="sr-only">${t("dashboard.search_label")}</span><input id="search" type="search" placeholder="${t("dashboard.search")}" autocomplete="off"></label></div>
    <section id="devices" class="device-groups" aria-label="${t("dashboard.items")}"></section>
    <section class="panel history-panel" id="history-panel" hidden><div class="panel-heading"><div><h2 id="history-heading" tabindex="-1">${t("common.history")}</h2><p id="history-context"></p></div><label><span class="sr-only">${t("dashboard.period")}</span><select id="period" class="input"><option value="24">${t("dashboard.hours24")}</option><option value="6">${t("dashboard.hours6")}</option><option value="1">${t("dashboard.hour")}</option><option value="0">${t("dashboard.all_data")}</option></select></label></div><label for="metric-select">${t("common.measurement")}</label><select id="metric-select" class="input"></select><cj-chart id="history"></cj-chart><div class="plot-footer"><span class="legend" id="chart-legend"></span><span id="plot-count"></span></div></section>
    <footer class="footer"><span id="snapshot-time"></span><span>${t("dashboard.retention")}</span></footer>
    <dialog id="device-dialog" aria-labelledby="device-dialog-title"><div class="dialog-head"><h2 id="device-dialog-title">${t("dashboard.details")}</h2><button class="icon-button" id="close-dialog" aria-label="${t("dashboard.close_details")}">${icon("close")}</button></div><div id="device-detail"></div></dialog>`;

  function rebuild() {
    const now = Date.parse(snapshot.generated_at);
    groups = workspaceGroups(snapshot, now);
    if (snapshot.workspace) groups = registeredGroups(groups);
    channels = groups.flatMap((g) => [
      ...g.sensors.flatMap((s) => s.channels),
      ...g.diagnostics,
    ]);
    for (const c of channels) {
      c.updated = age(c.at, now);
      if (c.metric === "co2") c.title = t("metrics.co2");
      if (isLinkDiagnostic(c)) c.title = c.metric.toUpperCase();
    }
    if (!channels.some((c) => c.key === selected)) selected = "";
    // A transmitter whose receiver is offline cannot report: that needs attention too.
    const deviceStates = snapshot.device_states ?? [];
    for (const g of groups) {
      if (g.transport !== "mqtt") continue;
      const state = deviceStateFor(deviceStates, g.source, g.device);
      const receiver = state?.receiver_id
        ? deviceStateFor(deviceStates, g.source, state.receiver_id)
        : null;
      g.state = state;
      g.receiver = receiver;
      g.receiverOffline = receiver?.availability === "offline";
      if (g.receiverOffline) g.attention = true;
    }
  }
  function receivers() {
    return (snapshot.device_states ?? []).filter((s) => s.role === "receiver");
  }
  function renderReceivers() {
    const target = root.querySelector("#receivers");
    const list = receivers();
    target.hidden = !list.length;
    target.innerHTML = list.length
      ? `<h2>${t("receivers.heading")}</h2><div class="receiver-items"></div>`
      : "";
    const now = Date.parse(snapshot.generated_at);
    for (const r of list) {
      const summary = receiverSummary(r);
      const transmitters = (snapshot.device_states ?? []).filter(
        (s) =>
          s.role === "transmitter" &&
          s.source_id === r.source_id &&
          s.receiver_id === r.device_id &&
          s.binding !== "revoked",
      ).length;
      const wifi = r.wifi?.rssi_dbm;
      const rows = [
        [t("receivers.firmware"), firmwareText(r.firmware)],
        [t("receivers.uptime"), uptimeText(r.uptime_s)],
        [
          t("receivers.wifi"),
          typeof wifi === "number"
            ? `${formatValue(wifi, 0)} dBm`
            : t("common.unknown"),
        ],
        [
          t("receivers.queue"),
          r.queue && typeof r.queue.depth === "number"
            ? t("receivers.queue_value", {
                depth: formatValue(r.queue.depth, 0),
                capacity: formatValue(r.queue.capacity, 0),
              })
            : t("common.unknown"),
        ],
        [
          t("receivers.forwarded"),
          typeof r.forwarding?.published === "number"
            ? t("receivers.readings", {
                count: r.forwarding.published,
              })
            : t("common.unknown"),
        ],
        [
          t("receivers.retries"),
          typeof r.forwarding?.retries === "number"
            ? formatValue(r.forwarding.retries, 0)
            : t("common.unknown"),
        ],
        [
          t("receivers.reset"),
          r.reset_reason
            ? known(
                `receivers.reset_reasons.${r.reset_reason}`,
                t("receivers.reset_reasons.other"),
              )
            : t("common.unknown"),
        ],
        [
          t("receivers.availability_changed"),
          // A retained snapshot's time is when Central connected, not when it changed.
          r.availability_at && !r.availability_retained
            ? age(r.availability_at, now)
            : t("common.unknown"),
        ],
      ];
      const badge = { online: "ok", offline: "error", unknown: "empty" }[
        summary.status
      ];
      const card = document.createElement("article");
      card.className = "panel receiver-card";
      card.dataset.status = summary.status;
      card.innerHTML = `<div class="device-identity"><span class="device-symbol">${icon("signal")}</span><div><h3>${e(receiverLabel(r.device_id))}</h3><p class="muted">${e(t("receivers.transmitters", { count: transmitters }))}</p></div><span class="badge" data-state="${badge}">${e(t(`receivers.${summary.status}`))}</span></div>${summary.notices.length ? `<ul class="receiver-notices">${summary.notices.map((n) => `<li data-level="${n.level}">${n.level === "info" ? "" : icon("alert")}<span>${e(n.text)}</span></li>`).join("")}</ul>` : ""}<dl class="detail-list receiver-details">${rows.map(([k, v]) => `<div><dt>${e(k)}</dt><dd>${e(v)}</dd></div>`).join("")}</dl>`;
      target.querySelector(".receiver-items").append(card);
    }
  }
  function matchingGroups() {
    const search = query.trim().toLowerCase();
    return groups.filter(
      (g) =>
        (filter !== "attention" || g.attention) &&
        [
          g.name,
          g.device,
          g.source,
          g.location,
          ...g.sensors.flatMap((s) => [
            s.name,
            ...s.channels.map((c) => c.title),
          ]),
        ]
          .join(" ")
          .toLowerCase()
          .includes(search),
    );
  }
  function deviceStatus(g) {
    return g.transport === "http"
      ? "recorded"
      : g.stale
        ? "stale"
        : g.interval
          ? "ok"
          : "empty";
  }
  function renderGroups() {
    const target = root.querySelector("#devices");
    target.replaceChildren();
    renderSections(target);
  }
  function renderSections(target) {
    const sections =
      snapshot.workspace.layout.sections ??
      automaticSections(snapshot.workspace);
    const matching = matchingGroups();
    for (const section of sections) {
      const block = document.createElement("section");
      block.className = "dashboard-section";
      block.setAttribute("aria-label", section.title);
      block.innerHTML = `<h2>${e(section.title)}</h2><div class="dashboard-items"></div>`;
      const items = block.querySelector(".dashboard-items");
      for (const item of section.items) {
        const resolved = resolveItem(item, matching);
        if (!resolved) continue;
        const { group: g, sensor, channels: readings } = resolved;
        const card = document.createElement("article");
        card.className = "panel dashboard-item";
        if (item.kind === "device") {
          card.classList.add("transmitter-card");
          card.innerHTML = `<div class="device-identity"><span class="device-symbol">${icon("device")}</span><div><h3>${e(g.name)}</h3><p class="muted">${e(t("counts.registered", { count: g.sensors.length }))}${g.location ? ` · ${e(g.location)}` : ""}</p></div></div><p class="device-last-report">${e(t("dashboard.last_report", { age: age(g.at, Date.parse(snapshot.generated_at)) }))}</p>${g.receiverOffline ? `<p class="device-alert">${icon("alert")}<span>${e(t("receivers.receiver_offline"))}</span></p>` : ""}<div class="transmitter-footer"><cj-badge state="${deviceStatus(g)}"></cj-badge><button class="text-button" data-details aria-label="${e(t("dashboard.details_for", { name: g.name }))}">${t("dashboard.details_short")}${icon("arrow")}</button></div>`;
          card
            .querySelector("[data-details]")
            .addEventListener("click", () => showDevice(g));
        } else {
          card.classList.add("sensor-group");
          if (readings.length > 1) card.classList.add("sensor-group-wide");
          card.innerHTML = `<header class="sensor-group-heading"><div><h3>${e(sensor.name)}</h3><p class="muted">${e(g.name)}${sensor.location ? ` · ${e(sensor.location)}` : ""}</p></div></header><div class="reading-grid"></div>`;
          for (const c of readings)
            card.querySelector(".reading-grid").append(readingButton(c, g));
        }
        items.append(card);
      }
      if (!items.children.length)
        items.innerHTML = `<p class="muted">${t("dashboard.no_items")}</p>`;
      target.append(block);
    }
    if (!sections.length) {
      const available = snapshot.workspace.devices.filter(
        (d) => !d.name,
      ).length;
      target.innerHTML = `<div class="empty">${icon("overview")}<h2>${t("dashboard.make_yours")}</h2><p>${available ? t("counts.detected", { count: available }) + " " : ""}${t("dashboard.get_started")}</p><div class="top-actions"><a class="button primary" href="/devices">${t("dashboard.manage_devices")}</a><a class="button" href="/sensors">${t("dashboard.manage_sensors")}</a></div></div>`;
    }
  }
  function readingButton(c, g) {
    const button = document.createElement("button");
    button.className = "reading-button";
    button.dataset.channelKey = c.key;
    button.setAttribute(
      "aria-label",
      t("dashboard.inspect", {
        measurement: c.title,
        value: formatValue(
          ["ok", "recorded", "stale"].includes(c.state) ? c.value : null,
        ),
        unit: formatUnit(c.unit),
        status: states[c.state],
        sensor: c.sensorName ?? c.sensor,
        device: g.name,
      }),
    );
    button.setAttribute("aria-pressed", String(c.key === selected));
    const reading = document.createElement("cj-reading");
    reading.data = c;
    button.append(reading);
    button.addEventListener("click", () => selectHistory(c.key));
    return button;
  }
  function selectHistory(key) {
    selected = key;
    root.querySelector("#metric-select").value = key;
    renderChart();
    for (const button of root.querySelectorAll(".reading-button"))
      button.setAttribute(
        "aria-pressed",
        String(button.dataset.channelKey === key),
      );
    root.querySelector("#history-heading").focus({ preventScroll: true });
    root.querySelector("#history-panel").scrollIntoView({ block: "start" });
  }
  function renderChart() {
    const c = channels.find((c) => c.key === selected);
    root.querySelector("#history-panel").hidden = !c;
    if (!c) return;
    const points = c.points.filter(
      (p) =>
        !hours || p.time >= Date.parse(snapshot.generated_at) - hours * 3600000,
    );
    root.querySelector("#history").data = { ...c, label: c.title, points };
    root.querySelector("#history-panel").dataset.kind = metricIcon(c.metric);
    root.querySelector("#history-heading").textContent = t(
      "dashboard.history_title",
      { measurement: c.title },
    );
    root.querySelector("#history-context").textContent = t(
      "dashboard.history_context",
      {
        device: c.deviceName ?? c.device,
        sensor: isLinkDiagnostic(c)
          ? t("dashboard.radio")
          : t("dashboard.sensor_context", { sensor: c.sensorName ?? c.sensor }),
        source: c.source,
      },
    );
    root.querySelector("#chart-legend").textContent =
      `${c.title} · ${formatUnit(c.unit)}`;
    root.querySelector("#plot-count").textContent = t("counts.observations", {
      count: points.length,
    });
  }
  function showDevice(g) {
    const values = [
      [t("dashboard.device_id"), g.device],
      [t("common.source"), g.source],
      [t("common.transport"), g.transport.toUpperCase()],
      [
        t("common.last_report"),
        g.at ? new Date(g.at).toLocaleString(locale()) : t("common.unknown"),
      ],
      [
        t("dashboard.interval"),
        g.interval
          ? t("counts.seconds", { count: g.interval })
          : t("dashboard.not_reported"),
      ],
      [t("dashboard.registered_sensors"), g.sensors.length],
      [t("dashboard.arrival_status"), states[deviceStatus(g)]],
      ...(g.state
        ? [
            [
              t("receivers.binding"),
              known(
                `receivers.binding_${g.state.binding}`,
                t("common.unknown"),
              ),
            ],
            [
              t("receivers.receiver"),
              g.receiver
                ? `${receiverLabel(g.receiver.device_id)} · ${t(`receivers.${receiverSummary(g.receiver).status}`)}`
                : receiverLabel(g.state.receiver_id),
            ],
            [t("receivers.last_frame"), linkText(g.state.last_frame)],
          ]
        : []),
      ...g.diagnostics.map((c) => [
        c.title,
        `${formatValue(["ok", "recorded", "stale"].includes(c.state) ? c.value : null)} ${formatUnit(c.unit)} · ${states[c.state]}`,
      ]),
    ];
    root.querySelector("#device-detail").innerHTML =
      `<h3>${e(g.name)}</h3><dl class="detail-list">${values.map(([k, v]) => `<div><dt>${e(k)}</dt><dd>${e(v)}</dd></div>`).join("")}</dl><p class="dialog-note">${t("dashboard.identity_note")}</p>`;
    const diagnostics = document.createElement("div");
    diagnostics.className = "diagnostic-readings";
    for (const c of g.diagnostics) {
      const button = document.createElement("button");
      button.className = "diagnostic-button";
      button.setAttribute(
        "aria-label",
        t("dashboard.inspect_history", {
          measurement: c.title,
          device: g.name,
        }),
      );
      button.textContent = t("dashboard.history_title", {
        measurement: c.title,
      });
      button.addEventListener("click", () => {
        root.querySelector("#device-dialog").close();
        selectHistory(c.key);
      });
      diagnostics.append(button);
    }
    if (g.diagnostics.length)
      root.querySelector("#device-detail").append(diagnostics);
    root.querySelector("#device-dialog").showModal();
  }
  function render() {
    rebuild();
    const sensorCount = groups.reduce((n, g) => n + g.sensors.length, 0);
    const measurements = groups.reduce(
      (n, g) => n + g.sensors.reduce((sum, s) => sum + s.channels.length, 0),
      0,
    );
    const issues =
      groups.filter((g) => g.attention).length +
      receivers().filter((r) => r.availability === "offline").length;
    root.querySelector("#summary").innerHTML =
      `<p>${t("counts.devices", { count: groups.length })}<span aria-hidden="true"> / </span>${t("counts.sensors", { count: sensorCount })}<span aria-hidden="true"> / </span>${t("counts.measurements", { count: measurements })}</p>${issues ? `<span class="workspace-attention">${icon("alert")}${t("counts.issues", { count: issues })}</span>` : `<span class="muted">${groups.length ? t("dashboard.no_issues") : t("common.waiting")}</span>`}`;
    root.querySelector("#metric-select").innerHTML = channels
      .map(
        (c) =>
          `<option value="${e(c.key)}">${e(c.title)} · ${e(c.deviceName ?? c.device)} · ${e(c.sensorName ?? c.sensor)} · ${e(formatUnit(c.unit))} · ${e(c.source)}</option>`,
      )
      .join("");
    root.querySelector("#metric-select").value = selected;
    renderReceivers();
    renderGroups();
    renderChart();
    root.querySelector("#snapshot-time").textContent = t("dashboard.updated", {
      time: new Date(snapshot.generated_at).toLocaleTimeString(locale(), {
        hour: "2-digit",
        minute: "2-digit",
      }),
    });
  }
  async function refresh() {
    if (pending) return;
    pending = true;
    const button = root.querySelector("#refresh");
    button.disabled = true;
    try {
      const response = await fetch("/", {
        cache: "no-store",
        signal: AbortSignal.timeout(10000),
      });
      if (!response.ok) throw new Error("refresh failed");
      const doc = new DOMParser().parseFromString(
        await response.text(),
        "text/html",
      );
      const next = JSON.parse(doc.querySelector("#initial-state").textContent);
      if (
        !Array.isArray(next.samples) ||
        !Array.isArray(next.readings) ||
        !Array.isArray(next.devices)
      )
        throw new Error("invalid snapshot");
      snapshot = next;
      root.querySelector("#fetch-error").classList.add("hidden");
      render();
    } catch {
      root.querySelector("#fetch-error").textContent = t(
        "dashboard.refresh_error",
      );
      root.querySelector("#fetch-error").classList.remove("hidden");
    } finally {
      pending = false;
      button.disabled = false;
    }
  }

  root.querySelector("#organize").hidden = !snapshot.workspace;
  root
    .querySelector("#organize")
    .addEventListener("click", () => openLayoutEditor(root, snapshot));
  root.querySelectorAll("[data-filter]").forEach((button) =>
    button.addEventListener("click", () => {
      filter = button.dataset.filter;
      root
        .querySelectorAll("[data-filter]")
        .forEach((b) => b.setAttribute("aria-pressed", String(b === button)));
      renderGroups();
    }),
  );
  root.querySelector("#search").addEventListener("input", (event) => {
    query = event.target.value;
    renderGroups();
  });
  root.querySelector("#period").addEventListener("change", (event) => {
    hours = Number(event.target.value);
    renderChart();
  });
  root
    .querySelector("#metric-select")
    .addEventListener("change", (event) => selectHistory(event.target.value));
  root.querySelector("#refresh").addEventListener("click", refresh);
  root
    .querySelector("#close-dialog")
    .addEventListener("click", () =>
      root.querySelector("#device-dialog").close(),
    );
  root.querySelector("#export").addEventListener("click", () => {
    const keys = new Set(
      [...root.querySelectorAll(".reading-button")].map(
        (b) => b.dataset.channelKey,
      ),
    );
    const visible = channels.filter((c) => keys.has(c.key));
    const url = URL.createObjectURL(
      new Blob([csvRows(visible)], { type: "text/csv;charset=utf-8" }),
    );
    const a = document.createElement("a");
    a.href = url;
    a.download = "cajui-readings.csv";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    notify(t("dashboard.exported"));
  });
  render();
  const timer = setInterval(() => {
    if (
      !document.hidden &&
      !root.contains(document.activeElement) &&
      !root.querySelector("dialog[open]")
    )
      refresh();
  }, 30000);
  window.addEventListener("pagehide", () => clearInterval(timer), {
    once: true,
  });
}
