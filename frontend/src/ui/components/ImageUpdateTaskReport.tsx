import { t } from "../../i18n";
import { Tag } from "antd";

type ImageUpdateEntryState = "updating" | "verified" | "skipped" | "restored" | "failed";

export type ImageUpdateEntry = {
  account: string;
  fromImage: string;
  toImage: string;
  detail: string;
  state: ImageUpdateEntryState;
};

export type ImageUpdateReport = {
  entries: ImageUpdateEntry[];
  updatedCount: number | null;
  verifiedCount: number;
  skippedCount: number;
  targetImage: string;
  notices: string[];
  lineCount: number;
};

type TaskStatus = "queued" | "running" | "cancelling" | "succeeded" | "failed" | "cancelled";

export function ImageUpdateTaskReport({ output, status }: { output: string; status: TaskStatus }) {
  const report = parseImageUpdateOutput(output, status);
  if (!report) return null;
  const active = ["queued", "running", "cancelling"].includes(status);
  const discoveredUpdates = report.entries.filter((entry) => entry.fromImage && entry.toImage).length;
  const updatedCount = report.updatedCount ?? (status === "succeeded" ? discoveredUpdates : null);
  const targetImage = report.targetImage || (active ? t("common.identifying") : t("common.no_image_digest_in_output"));
  return (
    <section className="image-update-task-report" aria-label={t("common.cpa_image_update_details")}>
      <div className="image-update-summary" aria-label={t("common.image_update_summary")}>
        <div className="target">
          <span>{t("common.target_image")}</span>
          <code title={targetImage}>{targetImage}</code>
        </div>
        <div>
          <span>{t("common.affected_accounts")}</span>
          <strong>{report.entries.length}</strong>
        </div>
        <div>
          <span>{t("common.update_complete")}</span>
          <strong>{updatedCount ?? "—"}</strong>
        </div>
        <div>
          <span>{t("common.probes_passed")}</span>
          <strong>{report.verifiedCount}</strong>
        </div>
        <div>
          <span>{t("common.skipped")}</span>
          <strong>{report.skippedCount}</strong>
        </div>
      </div>

      <div className="image-update-account-list" role="list" aria-label={t("common.account_update_results")}>
        {report.entries.map((entry) => {
          const presentation = imageUpdateEntryPresentation(entry, status);
          return (
            <article className={`image-update-account ${presentation.tone}`} role="listitem" key={entry.account}>
              <header>
                <strong title={entry.account}>{entry.account}</strong>
                <Tag color={presentation.color}>{presentation.label}</Tag>
              </header>
              {entry.fromImage || entry.toImage ? (
                <div className="image-update-transition">
                  <div><span>{t("common.previous_image")}</span><code title={entry.fromImage}>{entry.fromImage || "—"}</code></div>
                  <i aria-hidden="true">→</i>
                  <div><span>{t("common.target_image")}</span><code title={entry.toImage}>{entry.toImage || "—"}</code></div>
                </div>
              ) : null}
              <p>{entry.detail || presentation.detail}</p>
            </article>
          );
        })}
      </div>

      {report.notices.length ? (
        <div className="image-update-notices" aria-label={t("common.task_notes")}>
          {report.notices.map((notice, index) => <p key={`${index}-${notice}`}>{notice}</p>)}
        </div>
      ) : null}

      <details className="image-update-raw-output">
        <summary>{t("common.view_raw_output")} <span>{report.lineCount} {t("common.lines")}</span></summary>
        <pre className="oauth-task-output">{output}</pre>
      </details>
    </section>
  );
}

export function parseImageUpdateOutput(output: string, status: TaskStatus = "succeeded"): ImageUpdateReport | null {
  const lines = output.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const entries = new Map<string, ImageUpdateEntry>();
  const notices: string[] = [];
  let updatedCount: number | null = null;
  let recognized = 0;

  const ensureEntry = (account: string) => {
    const existing = entries.get(account);
    if (existing) return existing;
    const created: ImageUpdateEntry = {
      account,
      fromImage: "",
      toImage: "",
      detail: "",
      state: "updating"
    };
    entries.set(account, created);
    return created;
  };

  for (const line of lines) {
    const updating = line.match(/^(?:正在更新|Updating)\s+(.+?)[:：]\s*(.+?)\s+->\s+(.+)$/);
    if (updating) {
      const entry = ensureEntry(updating[1].trim());
      entry.fromImage = updating[2].trim();
      entry.toImage = updating[3].trim();
      entry.state = "updating";
      entry.detail = t("common.image_replaced_waiting_for_runtime_probes");
      recognized++;
      continue;
    }
    const verified = line.match(/^(.+?)\s+(?:验证通过|verified)[:：]\s*(.+)$/);
    if (verified) {
      const entry = ensureEntry(verified[1].trim());
      entry.state = "verified";
      entry.detail = t("common.passed", [verified[2].trim()]);
      recognized++;
      continue;
    }
    const skipped = line.match(/^(?:跳过|Skipping)\s+(.+?)[:：]\s*(.+)$/);
    if (skipped) {
      const entry = ensureEntry(skipped[1].trim());
      entry.state = "skipped";
      entry.detail = skipped[2].trim();
      recognized++;
      continue;
    }
    const restored = line.match(/^(?:已恢复|Restored)\s+(.+)$/);
    if (restored) {
      const entry = ensureEntry(restored[1].trim());
      entry.state = "restored";
      entry.detail = t("common.update_failed_original_image_restored_and_probes_passed");
      recognized++;
      continue;
    }
    const completed = line.match(/^(?:CPA 镜像更新完成|CPA image update completed)[:：]\s*(\d+)\s*(?:个)?$/);
    if (completed) {
      updatedCount = Number(completed[1]);
      notices.push(line);
      recognized++;
      continue;
    }
    if (/^(运行中的 CPA 已验证|没有运行中的 CPA|镜像更新失败|Running CPAs verified|No running CPAs|Image update failed)/.test(line)) {
      notices.push(line);
      recognized++;
    }
  }

  if (!recognized) return null;
  const normalizedEntries = [...entries.values()].map((entry) => (
    status === "failed" && entry.state === "updating"
      ? { ...entry, state: "failed" as const, detail: t("common.image_update_incomplete_check_the_task_notes_and_raw_output") }
      : entry
  ));
  return {
    entries: normalizedEntries,
    updatedCount,
    verifiedCount: normalizedEntries.filter((entry) => entry.state === "verified").length,
    skippedCount: normalizedEntries.filter((entry) => entry.state === "skipped").length,
    targetImage: normalizedEntries.find((entry) => entry.toImage)?.toImage ?? "",
    notices,
    lineCount: lines.length
  };
}

function imageUpdateEntryPresentation(entry: ImageUpdateEntry, taskStatus: TaskStatus) {
  switch (entry.state) {
    case "verified":
      return { label: entry.fromImage ? t("common.updated_verified") : t("common.verified"), color: "success", tone: "success", detail: t("common.runtime_probes_passed") };
    case "skipped":
      return { label: t("common.skipped"), color: "default", tone: "neutral", detail: t("common.this_account_s_image_was_not_replaced") };
    case "restored":
      return { label: t("common.restored"), color: "warning", tone: "warning", detail: t("common.original_image_restored") };
    case "failed":
      return { label: t("common.update_failed"), color: "error", tone: "error", detail: t("common.image_update_incomplete") };
    default:
      return taskStatus === "cancelled"
        ? { label: t("common.cancelled"), color: "default", tone: "neutral", detail: t("common.task_cancelled") }
        : { label: t("common.updating"), color: "processing", tone: "processing", detail: t("common.waiting_for_runtime_probes") };
  }
}
