import "../i18n/usage";
import { t } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp } from "./site-time";
import {
  Alert,
  Button,
  Card,
  Col,
  Drawer,
  Empty,
  Result,
  Row,
  Space,
  Statistic,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import {
  portalBreakdownQueryKey,
  readPortalBreakdown,
  type PortalUsageWindow
} from "../api/portal";
import type { UsageMetrics } from "../api/usage";
import { AdminTable } from "./components/AdminTable";
import { WideSelect } from "./components/WideSelect";
import { formatTokens } from "./formatters";

type ModelRow = UsageMetrics & { model: string };

export function PortalUsageBreakdownDrawer({
  open,
  account,
  displayName,
  onClose
}: {
  open: boolean;
  account: string;
  displayName: string;
  onClose: () => void;
}) {
  useSiteTimezone();
  const [window, setWindow] = useState<PortalUsageWindow>("86400");
  const query = useQuery({
    queryKey: portalBreakdownQueryKey(account, window),
    queryFn: ({ signal }) => readPortalBreakdown(account, window, signal),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true,
    refetchInterval: open ? 10_000 : false
  });

  return (
    <Drawer
      title={t("usage.usage_details", [displayName || t("common.all_accounts")])}
      open={open}
      size={760}
      onClose={onClose}
      destroyOnHidden
      extra={(
        <Space wrap>
          <WideSelect<PortalUsageWindow>
            aria-label={t("usage.usage_time_range")}
            value={window}
            options={portalWindowOptions}
            onChange={setWindow}
          />
          <Button
            icon={<ReloadOutlined aria-hidden="true" />}
            loading={query.isFetching}
            onClick={() => void query.refetch()}
          >
 {t("usage.refresh_now")} </Button>
        </Space>
      )}
    >
      <Space orientation="vertical" size={16} className="usage-drawer-content">
        <Alert
          type="info"
          showIcon
          title={t("usage.live_queries_on_demand")}
          description={t("usage.reads_the_current_sqlite_snapshot_only_while_the_drawer_is")}
        />
        {query.isPending ? <Card loading /> : null}
        {query.isError ? (
          <Result
            status="warning"
            title={t("usage.unable_to_load_usage_details")}
            subTitle={query.error instanceof Error ? query.error.message : t("common.please_try_again_later")}
            extra={<Button type="primary" onClick={() => void query.refetch()}>{t("common.reload")}</Button>}
          />
        ) : null}
        {query.data ? <PortalBreakdownContent data={query.data} /> : null}
      </Space>
    </Drawer>
  );
}

function PortalBreakdownContent({ data }: { data: Awaited<ReturnType<typeof readPortalBreakdown>> }) {
  if (!data.collection_started_at) {
    return <Empty description={t("usage.detailed_collection_has_not_started")} />;
  }
  return (
    <>
      <Row gutter={[12, 12]}>
        <Col xs={12} lg={6}><Card><Statistic title={t("usage.requests")} value={data.totals.request_count} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title={t("common.succeeded")} value={data.totals.success_count} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title={t("common.failed")} value={data.totals.failed_count} /></Card></Col>
        <Col xs={12} lg={6}>
          <Card>
            <Statistic
              title={t("common.weighted_tokens_2")}
              value={data.totals.weighted_tokens ?? 0}
              formatter={(value) => formatTokens(Number(value))}
            />
          </Card>
        </Col>
      </Row>
      <Card
        title={t("usage.model_usage")}
        extra={<Typography.Text type="secondary">{t("usage.data_timestamp")}{formatSiteTimestamp(data.generated_at)}</Typography.Text>}
      >
        <AdminTable<ModelRow>
          rowKey="model"
          columns={modelColumns}
          dataSource={data.models}
          pagination={false}
          locale={{ emptyText: t("usage.no_model_details_in_this_range") }}
          scroll={{ x: 620 }}
          maxBodyHeight="min(48vh, 480px)"
          size="small"
        />
      </Card>
      <Card title={t("usage.reasoning_effort")}>
        <Space wrap>
          {data.reasoning_efforts.length === 0 ? (
            <Typography.Text type="secondary">{t("usage.no_reasoning_effort_details_in_this_range")}</Typography.Text>
          ) : data.reasoning_efforts.map((item) => (
            <Tag key={item.reasoning_effort}>
              {effortLabels[item.reasoning_effort] ?? item.reasoning_effort} · {formatTokens(item.weighted_tokens ?? 0)}
            </Tag>
          ))}
        </Space>
      </Card>
    </>
  );
}

const modelColumns: TableColumnsType<ModelRow> = [
  { title: t("common.model"), dataIndex: "model", width: 220 },
  { title: t("common.requests"), dataIndex: "request_count", align: "right", width: 90 },
  {
    title: t("common.weighted_tokens_2"),
    align: "right",
    width: 140,
    render: (_, item) => formatTokens(item.weighted_tokens ?? 0)
  },
  { title: t("common.last_used"), dataIndex: "last_used_at", width: 170, render: (timestamp: number) => formatSiteTimestamp(timestamp) }
];

const portalWindowOptions: Array<{ value: PortalUsageWindow; label: string }> = [
  { value: "today", label: t("usage.today") },
  { value: "3600", label: t("usage.last_hour") },
  { value: "86400", label: t("usage.last_24_hours") },
  { value: "604800", label: t("common.last_7_days_2") },
  { value: "2592000", label: t("common.last_30_days") }
];

const effortLabels: Record<string, string> = {
  none: t("common.none"),
  minimal: t("common.minimal"),
  low: t("common.low"),
  medium: t("common.medium"),
  high: t("common.high"),
  xhigh: t("common.ultra"),
  max: t("common.max"),
  ultra: t("common.extra_high"),
  auto: t("common.auto"),
  unknown: t("common.unknown")
};
