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
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import {
  readUsageBreakdown,
  usageBreakdownQueryKey,
  type UsageCombination,
  type UsageWindow
} from "../api/usage";
import { AdminTable } from "./components/AdminTable";
import { MetricCard } from "./components/MetricCard";
import { TokenValue } from "./components/TokenValue";
import { WideSelect } from "./components/WideSelect";

const { Text } = Typography;

export function UsageBreakdownDrawer({
  kind,
  subject,
  onClose
}: {
  kind: "account" | "user";
  subject: string | null;
  onClose: () => void;
}) {
  useSiteTimezone();
  const [window, setWindow] = useState<UsageWindow>("86400");
  const open = Boolean(subject);
  const query = useQuery({
    queryKey: usageBreakdownQueryKey(kind, subject ?? "", window),
    queryFn: ({ signal }) => readUsageBreakdown(kind, subject ?? "", window, signal),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true,
    refetchInterval: open ? 10_000 : false
  });

  return (
    <Drawer
      title={t("usage.usage_details_2", [kind === "account" ? t("usage.account") : t("common.user")])}
      open={open}
      size={760}
      onClose={onClose}
      destroyOnHidden
      extra={(
        <Space wrap>
          <WideSelect<UsageWindow>
            aria-label={t("usage.usage_time_range")}
            value={window}
            options={usageWindowOptions}
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
          message={t("usage.live_queries_on_demand")}
          description={t("usage.reads_the_current_sqlite_snapshot_every_10_seconds_while_open")}
        />
        <Text strong>{subject}</Text>
        {query.isPending ? <Card loading /> : null}
        {query.isError ? (
          <Result
            status="warning"
            title={t("usage.unable_to_load_usage_details_2")}
            subTitle={query.error instanceof Error ? query.error.message : t("common.please_try_again_later")}
            extra={<Button type="primary" onClick={() => void query.refetch()}>{t("common.reload")}</Button>}
          />
        ) : null}
        {query.data ? <UsageBreakdownContent kind={kind} data={query.data} /> : null}
      </Space>
    </Drawer>
  );
}

function UsageBreakdownContent({
  kind,
  data
}: {
  kind: "account" | "user";
  data: Awaited<ReturnType<typeof readUsageBreakdown>>;
}) {
  if (!data.collection_started_at) {
    return <Empty description={t("usage.detailed_collection_has_not_started")} />;
  }
  return (
    <>
      <Row gutter={[12, 12]}>
        <Col xs={12} lg={6}><MetricCard title={t("usage.requests")} value={data.totals.request_count} /></Col>
        <Col xs={12} lg={6}><MetricCard title={t("common.succeeded")} value={data.totals.success_count} /></Col>
        <Col xs={12} lg={6}><MetricCard title={t("common.failed")} value={data.totals.failed_count} /></Col>
        <Col xs={12} lg={6}>
          <MetricCard
            title={kind === "user" ? t("common.weighted_tokens_2") : "Token"}
            value={kind === "user" ? data.totals.weighted_tokens ?? 0 : data.totals.total_tokens}
            formatter={(value) => <TokenValue value={Number(value)} suffix="" />}
          />
        </Col>
      </Row>
      <Card
        title={t("common.model_reasoning_effort")}
        extra={<Text type="secondary">{t("usage.data_timestamp")}{formatSiteTimestamp(data.generated_at)}</Text>}
      >
        <AdminTable<UsageCombination>
          rowKey={(item) => `${item.account ?? "all"}:${item.model}:${item.reasoning_effort}`}
          columns={combinationColumns(kind)}
          dataSource={data.combinations}
          minWidth={700}
          maxBodyHeight="min(48vh, 480px)"
          size="small"
          emptyText={t("usage.no_model_details_in_this_range")}
        />
      </Card>
    </>
  );
}

function combinationColumns(kind: "account" | "user"): TableColumnsType<UsageCombination> {
  return [
    ...(kind === "user" ? [{
      title: t("usage.account"),
      dataIndex: "account" as const,
      width: 120
    }] : []),
    { title: t("common.model"), dataIndex: "model", width: 190 },
    {
      title: t("usage.reasoning_effort"),
      dataIndex: "reasoning_effort",
      width: 110,
      render: (value: string) => <Tag>{effortLabels[value] ?? value}</Tag>
    },
    { title: t("common.requests"), dataIndex: "request_count", align: "right", width: 80 },
    {
      title: kind === "user" ? t("common.weighted_tokens_2") : "Token",
      align: "right",
      width: 125,
      render: (_, item) => <TokenValue value={kind === "user" ? item.weighted_tokens ?? 0 : item.total_tokens} />
    },
    { title: t("common.last_used"), dataIndex: "last_used_at", width: 165, render: (timestamp: number) => formatSiteTimestamp(timestamp) }
  ];
}

const usageWindowOptions: Array<{ value: UsageWindow; label: string }> = [
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
