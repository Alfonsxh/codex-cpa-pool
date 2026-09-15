import "../../i18n/admin";
import { t } from "../../i18n";
import { Alert, Button, Modal, Select, Spin } from "antd";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { readAccountModels, testAccountModel, type Account } from "../../api/accounts";
import { formatSiteTimestamp } from "../site-time";

export function AccountModelTestModal({ account, csrfToken, onClose }: {
  account: Account;
  csrfToken: string;
  onClose: () => void;
}) {
  const [model, setModel] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const models = useQuery({
    queryKey: ["account-models", account.id],
    queryFn: ({ signal }) => readAccountModels(account.id, signal),
    retry: false, gcTime: 0, staleTime: 0, refetchOnWindowFocus: false
  });
  const options = models.data?.models ?? [];
  const selected = options.includes(model) ? model : options.includes("gpt-6-astra") ? "gpt-6-astra" : options[0] ?? "";
  const test = useMutation({
    mutationFn: async () => {
      controller.current = new AbortController();
      return testAccountModel(account.id, selected, csrfToken, controller.current.signal);
    },
    retry: false
  });
  const result = test.data;
  return <Modal open title={t("common.test_model_connection")} className="account-model-test-modal" width={560}
    onCancel={onClose} footer={[
      <Button key="close" onClick={onClose}>{test.isPending ? t("common.cancel_test") : t("common.close")}</Button>,
      <Button key="test" type="primary" loading={test.isPending} disabled={!selected || models.isFetching || models.isError}
        onClick={() => { test.reset(); test.mutate(); }}>{result ? t("common.test_again") : t("common.start_test")}</Button>
    ]}>
    <div className="account-model-test-content">
      <div className="account-model-test-account"><strong>{account.id}</strong><span>{account.email}</span></div>
      <label className="account-model-test-select"><span>{t("common.test_model")}</span>
        <Select aria-label={t("common.test_model")} showSearch value={selected || undefined} loading={models.isFetching}
          disabled={test.isPending || models.isFetching || models.isError} options={options.map(value => ({ value, label: value }))}
          placeholder={t("common.select_a_model")} onChange={value => { setModel(value); test.reset(); }} />
      </label>
      <p className="field-help">{t("common.send_a_short_generation_request_through_this_account_to_verify")}</p>
      {models.isError ? <Alert type="error" showIcon title={t("common.unable_to_load_models")} description={models.error.message}
        action={<Button size="small" onClick={() => void models.refetch()}>{t("common.retry")}</Button>} /> : null}
      {!models.isPending && !models.isError && options.length === 0 ? <Alert type="warning" showIcon title={t("common.this_account_has_no_models_available_to_test")} /> : null}
      {test.isPending ? <div className="account-model-test-pending" role="status"><Spin size="small" />{t("common.testing")} {selected}…</div> : null}
      {test.isError ? <Alert type="error" showIcon title={t("common.test_incomplete")} description={test.error.message} /> : null}
      {result && !test.isPending ? <div role="status" className="account-model-test-result">
        <Alert type={result.success ? "success" : "error"} showIcon title={result.success ? t("common.model_connection_succeeded") : t("common.model_connection_failed")} description={result.message} />
        <dl><div><dt>{t("common.model")}</dt><dd>{result.model}</dd></div>
          <div><dt>{t("common.duration")}</dt><dd>{(result.elapsed_ms / 1000).toFixed(2)} {t("common.sec")}</dd></div>
          <div><dt>{t("common.http_status")}</dt><dd>{result.upstream_status || t("common.no_response")}</dd></div>
          <div><dt>{t("common.test_time")}</dt><dd>{formatSiteTimestamp(result.checked_at)}</dd></div></dl>
      </div> : null}
    </div>
  </Modal>;
}
