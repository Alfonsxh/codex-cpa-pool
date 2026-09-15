import { t } from "../../i18n";
import { Button, Result, Skeleton } from "antd";

export type PageStateProps = {
  kind: "loading" | "error";
  title?: string;
  detail?: string;
  actionLabel?: string;
  onAction?: () => void;
  rows?: number;
};

export function PageState({
  kind,
  title = t("common.unable_to_load_page"),
  detail = t("common.please_try_again_later"),
  actionLabel = t("common.reload"),
  onAction,
  rows = 8
}: PageStateProps) {
  if (kind === "loading") {
    return (
      <div className="admin-page-state" aria-label={title || t("common.loading_page_2")}>
        <Skeleton active paragraph={{ rows }} />
      </div>
    );
  }
  return (
    <Result
      className="admin-page-state"
      status="warning"
      title={title}
      subTitle={detail}
      extra={onAction ? <Button type="primary" onClick={onAction}>{actionLabel}</Button> : null}
    />
  );
}
