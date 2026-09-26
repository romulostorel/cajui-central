import { t } from "./i18n.mjs";
// Management channel commands (cajui-firmware docs/management-v1.md), sent through the
// local page's capability like workspace edits.
export async function sendCommand(state, command) {
  let response;
  try {
    response = await fetch("/ui-api/commands", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Cajui-Workspace": state.ui_token,
      },
      body: JSON.stringify(command),
      signal: AbortSignal.timeout(10000),
    });
  } catch {
    throw new Error(t("commands.errors.send"));
  }
  if (!response.ok) {
    if (response.status === 403) throw new Error(t("errors.session"));
    if (response.status === 503) throw new Error(t("commands.errors.offline"));
    throw new Error(t("commands.errors.send"));
  }
  return response.json();
}
// Polls until a final answer, or until the server reports the command undelivered (it
// does so within minutes); the attempt bound only guards against a stuck server.
export async function awaitAnswer(
  record,
  { onPending, interval = 1000, attempts = 240 } = {},
) {
  let pendingSeen = false;
  for (let attempt = 0; ; attempt++) {
    if (record.status === "pending" && !pendingSeen) {
      pendingSeen = true;
      onPending?.(record);
    }
    if (!["sent", "pending"].includes(record.status)) return record;
    if (attempt >= attempts) throw new Error(t("commands.errors.status"));
    await new Promise((resolve) => setTimeout(resolve, interval));
    try {
      const response = await fetch(
        `/ui-api/commands/${encodeURIComponent(record.command_id)}`,
        { cache: "no-store", signal: AbortSignal.timeout(10000) },
      );
      if (!response.ok) throw new Error("status");
      record = await response.json();
    } catch {
      throw new Error(t("commands.errors.status"));
    }
  }
}
// Plain text for an answer; the operator reads it in a notice.
export function answerText(record) {
  if (record.status === "applied")
    return t(`commands.applied.${record.type.replace(".", "_")}`);
  if (record.status === "undelivered") return t("commands.undelivered");
  const reason = record.reason ?? "failed";
  const text = t(`commands.reasons.${reason}`);
  return text === `commands.reasons.${reason}`
    ? t("commands.reasons.failed")
    : text;
}
