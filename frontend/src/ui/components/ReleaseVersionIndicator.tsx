import { t } from "../../i18n";
import { useQuery } from "@tanstack/react-query";
import { Tooltip } from "antd";

import { readReleaseStatus } from "../../api/overview";

const stableVersionPattern = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

export function ReleaseVersionIndicator({ className = "" }: { className?: string }) {
  const release = useQuery({
    queryKey: ["admin-release-status"],
    queryFn: ({ signal }) => readReleaseStatus(false, signal),
    retry: false,
    refetchInterval: 15 * 60 * 1_000,
    refetchOnWindowFocus: false
  });
  const status = release.data;
  const currentVersion = status?.current_version.trim() || "";
  const latestVersion = status?.latest_version?.trim() || "";
  const updateAvailable = !release.isError && status?.status === "ok"
    && stableVersionPattern.test(latestVersion) && status.available;
  const versionLabel = currentVersion || (release.isPending ? t("common.reading_version") : t("common.unknown_version"));
  const indicatorClass = ["release-version-indicator", className].filter(Boolean).join(" ");

  return (
    <Tooltip
      title={updateAvailable ? (
        <div className="release-version-comparison">
          <span>{t("common.current_version")}</span><strong>{versionLabel}</strong>
          <span>{t("common.latest_version")}</span><strong>{latestVersion}</strong>
        </div>
      ) : null}
      classNames={{ root: "release-version-tooltip" }}
      placement="topRight"
      align={{ offset: [16, -8] }}
      trigger={["hover", "focus"]}
      arrow={false}
    >
      <span className={indicatorClass} data-update={updateAvailable ? "true" : "false"}
        tabIndex={updateAvailable ? 0 : undefined}
        aria-label={t("common.current_version_2", [versionLabel, updateAvailable ? t("common.update_available") : ""])}>
        {updateAvailable ? <span className="release-version-heartbeat" aria-hidden="true" /> : null}
        <span className="release-version-number">{versionLabel}</span>
      </span>
    </Tooltip>
  );
}
