import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AccountsPage } from "./AccountsPage";

const catalog = {
  accounts: [
    {
      id: "alpha",
      email: "alpha@example.com",
      port: 18319,
      proxy_mode: "inherit",
      enabled: true,
      default: true,
      runtime_state: "running",
      oauth_configured: true,
      associated_users: 2,
      routed_users: 3,
      active_users_1h: 2,
      active_user_emails_1h: ["alice@example.com", "bob@example.com"],
      reset_credit_count: 1,
      resettable: true,
      reset_window_labels: ["常规周限额"],
      usage_available: true,
      usage_window_start_at: 0,
      usage_window_available: true,
      usage: {
        request_count: 3,
        success_count: 2,
        failed_count: 1,
        input_tokens: 100,
        output_tokens: 20,
        reasoning_tokens: 10,
        cached_tokens: 0,
        total_tokens: 130,
        last_used_at: 90
      },
      proxy_configured: false,
      account_state: {
        account: "alpha",
        eligible: true,
        exhausted: false,
        reason: "available",
        used_percent: 20,
        remaining_percent: 80,
        headroom: 75,
        reset_at: 0,
        observed_at: 100
      },
      state_available: true
    },
    {
      id: "beta",
      email: "beta@example.com",
      port: 18320,
      proxy_mode: "direct",
      enabled: true,
      default: false,
      runtime_state: "stopped",
      oauth_configured: false,
      associated_users: 1,
      routed_users: 1,
      active_users_1h: 1,
      active_user_emails_1h: ["carol@example.com"],
      reset_credit_count: 0,
      resettable: false,
      reset_window_labels: [],
      usage_available: true,
      usage_window_start_at: 0,
      usage_window_available: true,
      usage: {
        request_count: 2,
        success_count: 2,
        failed_count: 0,
        input_tokens: 80,
        output_tokens: 10,
        reasoning_tokens: 5,
        cached_tokens: 0,
        total_tokens: 95,
        last_used_at: 80
      },
      proxy_configured: false,
      account_state: {
        account: "beta",
        eligible: true,
        exhausted: false,
        reason: "available",
        used_percent: 10,
        remaining_percent: 90,
        headroom: 85,
        reset_at: 0,
        observed_at: 100
      },
      state_available: true
    }
  ],
  generated_at: 100,
  window: "today",
  window_seconds: null,
  window_start_at: 0,
  window_start_at_by_account: null,
  window_end_at: 100,
  window_timezone: "Asia/Shanghai",
  warnings: []
};

const accountBreakdown = {
  generated_at: 100,
  window: "today",
  window_seconds: null,
  window_start_at: 0,
  window_end_at: 100,
  collection_started_at: 1,
  effective_start_at: 1,
  definition: "account_model_reasoning_effort_tokens",
  account: "alpha",
  totals: catalog.accounts[0].usage,
  models: [],
  reasoning_efforts: [],
  combinations: [
    { ...catalog.accounts[0].usage, model: "gpt-5.6-sol", reasoning_effort: "high", total_tokens: 100 },
    { ...catalog.accounts[0].usage, model: "gpt-5.6-sol", reasoning_effort: "medium", total_tokens: 30 }
  ]
};

const accountImages = {
  target_image: "eceasy/cli-proxy-api:latest",
  update_channel: "eceasy/cli-proxy-api:latest",
  candidate: {},
  applied: {},
  local_image: {
    available: true,
    image_id: "sha256:target",
    short_id: "target",
    created: "",
    repo_digests: [],
    version: "v7.2.140",
    commit: "",
    built_at: "",
    resolved_ref: "eceasy/cli-proxy-api:latest"
  },
  accounts: catalog.accounts.map((account) => ({
    account: account.id,
    service: `cliproxy-${account.id}`,
    enabled: account.enabled,
    container_exists: true,
    running: account.runtime_state === "running",
    state: account.runtime_state,
    image_ref: "eceasy/cli-proxy-api:latest",
    image_id: "sha256:account",
    image_short_id: "87c0bc86d4a8",
    version: "v7.2.140",
    using_target: true,
    rollback_available: true
  })),
  running_count: 1,
  current_count: 2,
  outdated_count: 0,
  cached: false
};

const accountImagesWithUpdate = {
  ...accountImages,
  accounts: accountImages.accounts.map((account) => account.account === "alpha"
    ? { ...account, using_target: false, image_id: "sha256:old-account", image_short_id: "old-account" }
    : account),
  current_count: 1,
  outdated_count: 1
};

describe("AccountsPage", () => {
  it("opens the create dialog from the first-run deep link", async () => {
    vi.stubGlobal("fetch", accountPageFetchMock(catalog));
    renderPage("/accounts?create=1");
    expect(await screen.findByText("添加业务 CPA")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("renders the legacy four-layer account expansion and loads detail only on demand", async () => {
    const fetchMock = accountPageFetchMock(catalog);
    vi.stubGlobal("fetch", fetchMock);
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("usage-breakdown"))).toBe(false);
    fireEvent.click(screen.getByRole("row", { name: "展开 alpha" }));

    expect(await screen.findByText("模型 × 推理强度 Token 明细")).toBeInTheDocument();
    const expandedPanel = document.querySelector<HTMLElement>(".account-expanded-panel");
    expect(expandedPanel).not.toBeNull();
    const expanded = within(expandedPanel!);
    expect(expanded.getByText("上游邮箱")).toBeInTheDocument();
    expect(expanded.getByText("镜像版本")).toBeInTheDocument();
    expect(expanded.getByText("Token 总计")).toBeInTheDocument();
    expect(await expanded.findByText("gpt-5.6-sol")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("usage-breakdown"))).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => String(url) === "/admin/api/images/cliproxy")).toBe(true);
    const actionTexts = [...document.querySelectorAll(".account-detail-actions button")].map((button) => button.textContent?.trim());
    expect(actionTexts).toEqual([
      "模型通信测试", "查看日志", "编辑账号", "重新 OAuth", "镜像已同步",
      "重启容器", "迁移全部用户", "停止容器", "停用账号"
    ]);
    expect(expanded.getByRole("group", { name: "常用操作" }).querySelectorAll("button")).toHaveLength(4);
    expect(expanded.getByRole("group", { name: "容器维护" }).querySelectorAll("button")).toHaveLength(2);
    expect(expanded.getByRole("group", { name: "风险操作" }).querySelectorAll("button")).toHaveLength(3);
    expect(actionTexts).not.toContain("更多");
  }, 10_000);

  it("uses the legacy default quota ordering and supports row click, Enter, and Space", async () => {
    vi.stubGlobal("fetch", accountPageFetchMock(catalog));
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    const quotaHeader = screen.getByRole("columnheader", { name: /额度与重置/ });
    expect(quotaHeader).toHaveAttribute("aria-sort", "ascending");
    const accountNames = [...document.querySelectorAll(".account-name-cell .table-primary")].map((node) => node.textContent);
    expect(accountNames).toEqual(["beta", "alpha"]);

    const currentAlphaRow = () => screen.getByText("alpha", { selector: ".account-name-cell .table-primary" }).closest("tr");
    expect(currentAlphaRow()).not.toBeNull();
    fireEvent.click(currentAlphaRow()!.querySelector("td:nth-child(3)")!);
    expect(await screen.findByText("模型 × 推理强度 Token 明细")).toBeInTheDocument();
    fireEvent.click(currentAlphaRow()!.querySelector("td:nth-child(3)")!);
    await waitFor(() => expect(currentAlphaRow()).toHaveAttribute("aria-expanded", "false"));
    fireEvent.click(currentAlphaRow()!.querySelector("td:nth-child(3)")!);
    await waitFor(() => expect(currentAlphaRow()).toHaveAttribute("aria-expanded", "true"));
    fireEvent.keyDown(currentAlphaRow()!, { key: "Enter" });
    await waitFor(() => expect(currentAlphaRow()).toHaveAttribute("aria-expanded", "false"));
    fireEvent.keyDown(currentAlphaRow()!, { key: " " });
    await waitFor(() => expect(currentAlphaRow()).toHaveAttribute("aria-expanded", "true"));
  });

  it("shows one-column rolling-hour users and the independent activity definition tooltip", async () => {
    vi.stubGlobal("fetch", accountPageFetchMock(catalog));
    const user = userEvent.setup();
    renderPage();

    const activeCount = await screen.findByText("2", { selector: ".account-active-users > strong" });
    await user.hover(activeCount);
    const alice = await screen.findByText("alice@example.com", { selector: ".account-active-user-email" });
    expect(alice.parentElement?.querySelectorAll(".account-active-user-email")).toHaveLength(2);
    expect(alice.parentElement).toHaveTextContent("bob@example.com");
    await user.unhover(activeCount);
    const helpText = "过去近 15 分钟内至少发起 1 次业务请求的去重用户；成功和失败请求均计入。";
    const help = screen.getAllByRole("button", { name: helpText })[1];
    await user.hover(help);
    const tooltip = await screen.findByRole("tooltip", { name: helpText });
    // Real-browser coverage checks placement and visibility; JSDOM has no layout.
    expect(tooltip).toHaveTextContent(helpText);
    expect(tooltip.closest(".ant-table-wrapper")).toBeNull();
    expect(document.body).toContainElement(tooltip);
  });

  it("loads quota-reset details only after click and submits the selected credit", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts/quota-reset?account=alpha": {
        account: "alpha",
        available_count: 1,
        details_truncated: false,
        windows: [{ key: "default:primary_window", label: "常规周限额", previous_reset_at: 100 }],
        credits: [{ id: "credit-1", title: "Full reset", expires_at: 1_800_000_000 }]
      },
      "/admin/api/accounts/reset-quota": {
        message: "周限额已重置，共刷新 1 个窗口",
        account: "alpha",
        windows: [{ key: "default:primary_window", label: "常规周限额", previous_reset_at: 100 }],
        windows_reset: 1,
        code: "rate_limit_reset_credit_consumed",
        credit: { title: "Full reset", status: "redeemed" }
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("剩余 1 次")).toBeInTheDocument();
    expect(requestsTo(fetchMock, "/admin/api/accounts/quota-reset?account=alpha")).toHaveLength(0);
    const resetButton = screen.getAllByRole("button", { name: "重置" }).find((button) => !button.hasAttribute("disabled"));
    expect(resetButton).toBeDefined();
    await user.click(resetButton!);
    expect(await screen.findByText("重置 alpha 周限额")).toBeInTheDocument();
    expect(screen.getByText("WEEKLY QUOTA RESET")).toBeInTheDocument();
    expect((await screen.findAllByText(/Full reset/)).length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "确认重置" }));

    expect(await screen.findByText("周限额已重置，共刷新 1 个窗口")).toBeInTheDocument();
    const mutation = requestsTo(fetchMock, "/admin/api/accounts/reset-quota")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({ account: "alpha", credit_id: "credit-1", confirm: "alpha" });
  });

  it("starts a stopped account without confirmation and opens the shared task output", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/operations": {
        message: "任务已提交",
        reused: false,
        job: legacyJob({
          id: "job-start", name: "启动服务", target: "beta", status: "succeeded",
          started_at: 101, finished_at: 102, exit_code: 0, output: ["service beta started"]
        })
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 beta" }));
    await user.click(screen.getByRole("button", { name: "启动容器" }));
    expect(await screen.findByText("TASK OUTPUT")).toBeInTheDocument();
    expect(screen.getByText("service beta started")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "复制完整输出" })).toBeInTheDocument();
    expect(screen.queryByText("确认容器操作")).not.toBeInTheDocument();
    const mutation = requestsTo(fetchMock, "/admin/api/operations")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({ action: "start", target: "beta" });
  });

  it("uses legacy restart and stop confirmations before opening task output", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/operations/impact?action=stop&target=alpha": {
        action: "stop", target: "alpha", target_type: "account", routed_users: 3
      },
      "/admin/api/operations": {
        message: "任务已提交",
        reused: false,
        job: legacyJob({
          id: "job-stop", name: "停止服务", target: "alpha", status: "succeeded",
          finished_at: 102, exit_code: 0, output: ["stopped"]
        })
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "重启容器" }));
    expect(await screen.findByText("重启服务？")).toBeInTheDocument();
    expect(screen.getByText("将重启 alpha。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /取\s*消/ }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "重启服务？" })).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "停止容器" }));
    expect(await screen.findByText("停止服务？")).toBeInTheDocument();
    expect(await screen.findByText("将停止 alpha，当前有 3 个用户路由到该账号。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认停止" }));
    expect(await screen.findByText("TASK OUTPUT")).toBeInTheDocument();
  }, 10_000);

  it("shows account logs in the legacy service-output modal", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/logs?target=alpha": { target: "alpha", output: "line one\nline two", exit_code: 0, truncated: false }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    const expandedPanel = document.querySelector<HTMLElement>(".account-expanded-panel");
    expect(expandedPanel).not.toBeNull();
    await user.click(within(expandedPanel!).getByRole("button", { name: "查看日志" }));
    expect(await screen.findByText("alpha 日志")).toBeInTheDocument();
    const logDialog = document.querySelector(".runtime-log-output")?.closest<HTMLElement>(".ant-modal");
    expect(logDialog).not.toBeNull();
    const logModal = within(logDialog!);
    expect(logModal.getByText("SERVICE LOGS")).toBeInTheDocument();
    expect(logModal.getByText("最近 200 行")).toBeInTheDocument();
    expect(logModal.getByText(/line one/)).toBeInTheDocument();
    expect(logModal.getByRole("button", { name: "复制完整输出" })).toBeInTheDocument();
    expect(logModal.queryByRole("button", { name: "刷新" })).not.toBeInTheDocument();
  });

  it("pulls the target image immediately without confirmation and opens task output", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/operations": {
        message: "镜像拉取任务已提交",
        reused: false,
        job: legacyJob({
          id: "job-image-pull", name: "拉取镜像", target: "all", status: "succeeded",
          finished_at: 101, exit_code: 0, output: ["pulled immutable image"]
        })
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("button", { name: "拉取镜像" }));

    expect(screen.queryByText("更新 CPA 镜像？")).not.toBeInTheDocument();
    expect(await screen.findByText("TASK OUTPUT")).toBeInTheDocument();
    expect(screen.getByText("pulled immutable image")).toBeInTheDocument();
    const mutation = requestsTo(fetchMock, "/admin/api/operations")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({
      action: "image-pull", target: "all"
    });
  });

  it("uses the exact legacy confirmation before updating every CPA image", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/images/cliproxy": accountImagesWithUpdate,
      "/admin/api/operations": {
        message: "镜像更新任务已提交",
        reused: false,
        job: legacyJob({
          id: "job-image-all", name: "更新 CPA 镜像", target: "all", status: "succeeded",
          started_at: 100, finished_at: 103, exit_code: 0,
          output: [
            "正在更新 alpha：sha256:old-account -> sha256:target",
            "alpha 验证通过：运行探针",
            "跳过 beta：CPA 未运行；下次启动会使用目标镜像",
            "CPA 镜像更新完成：1 个"
          ]
        })
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("button", { name: "更新全部 CPA" }));
    await screen.findByText("更新 CPA 镜像？");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("将使用已拉取并锁定版本与摘要的目标镜像逐个重建运行中的已启用 CPA，停用账号会跳过；失败时自动恢复原镜像。")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "更新全部 CPA" }));

    expect(await screen.findByLabelText("CPA 镜像更新详情")).toBeInTheDocument();
    const taskSummary = screen.getByLabelText("任务执行摘要");
    expect(taskSummary).toHaveTextContent("执行范围全部 CPA");
    expect(taskSummary).toHaveTextContent("执行状态成功");
    expect(taskSummary).toHaveTextContent("开始时间1970/01/01 08:01:40");
    expect(taskSummary).toHaveTextContent("完成时间1970/01/01 08:01:43");
    expect(taskSummary).toHaveTextContent("任务耗时3 秒");
    expect(taskSummary).toHaveTextContent("输出记录4 行");
    const imageSummary = screen.getByLabelText("镜像更新摘要");
    expect(imageSummary).toHaveTextContent("目标镜像sha256:target");
    expect(imageSummary).toHaveTextContent("涉及账号2");
    expect(imageSummary).toHaveTextContent("更新完成1");
    expect(imageSummary).toHaveTextContent("探针通过1");
    expect(imageSummary).toHaveTextContent("已跳过1");
    const accountResults = screen.getByRole("list", { name: "账号更新结果" });
    expect(within(accountResults).getByText("alpha")).toBeInTheDocument();
    expect(within(accountResults).getByText("beta")).toBeInTheDocument();
    expect(screen.getByText("查看原始输出").closest("details")).not.toHaveAttribute("open");
    const mutation = requestsTo(fetchMock, "/admin/api/operations")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({
      action: "image-update", target: "all"
    });
  });

  it("matches the legacy image failure by closing confirmation and showing an error toast", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/images/cliproxy": accountImagesWithUpdate,
      "/admin/api/operations": new Response(JSON.stringify({
        error: { code: "image_update_failed", message: "目标镜像重建失败，已恢复原镜像" }
      }), { status: 500, headers: { "Content-Type": "application/json" } })
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(await screen.findByRole("button", { name: "更新镜像" }));
    await screen.findByText("更新 CPA 镜像？");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("将使用已锁定版本与摘要的目标镜像重建 alpha；失败时自动恢复原镜像。")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "更新此 CPA" }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "更新 CPA 镜像？" })).not.toBeInTheDocument());
    expect(await screen.findByText("目标镜像重建失败，已恢复原镜像")).toHaveClass("toast", "error");
    const mutation = requestsTo(fetchMock, "/admin/api/operations")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({
      action: "image-update", target: "alpha"
    });
  });

  it("shows a runtime warning without disguising it as healthy migration capacity", async () => {
    const degradedCatalog = {
      ...catalog,
      accounts: catalog.accounts.map((account) => account.id === "alpha"
        ? { ...account, account_state: { ...account.account_state, reason: "degraded" } }
        : account)
    };
    vi.stubGlobal("fetch", accountPageFetchMock(degradedCatalog));
    renderPage();

    expect(await screen.findByText("近期异常")).toBeInTheDocument();
  });

  it("confirms global rebalance and refreshes only the account query", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts/rebalance-all": {
        message: "账号用户负载均衡已完成，近 15 分钟活跃用户数已刷新",
        rebalance: {
          moved_users: 1,
          destinations: { beta: 1 },
          target_counts: { alpha: 2, beta: 2 },
          snapshot_generation: "0123456789abcdef0123456789abcdef",
          active_users_1h: { alpha: 2, beta: 2 },
          activity_refreshed: true
        }
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
    });
    queryClient.setQueryData(["teams"], { teams: [{ id: "preserved" }] });
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <QueryClientProvider client={queryClient}>
          <AccountsPage csrfToken="csrf-test" />
        </QueryClientProvider>
      </MemoryRouter>
    );

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "一键负载均衡" }));
    expect(await screen.findByText("一键负载均衡所有账号")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认开始均衡" }));

    expect(await screen.findByText("迁移用户：1")).toBeInTheDocument();
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/accounts?window=today")).toHaveLength(2));
    const mutation = requestsTo(fetchMock, "/admin/api/accounts/rebalance-all")[0];
    expect(mutation).toBeDefined();
    expect(mutation[0]).toBe("/admin/api/accounts/rebalance-all");
    expect(mutation[1]).toMatchObject({
      method: "POST",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(queryClient.getQueryData(["teams"])).toEqual({ teams: [{ id: "preserved" }] });
  });

  it("uses the legacy account-policy endpoint and refreshes the account catalog", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts/policy": {
        message: "CPA 账号选择策略已更新",
        account: {
          account: {
            id: "alpha",
            email: "alpha@example.com",
            port: 18319,
            proxy_mode: "inherit",
            created_at: 100,
            group_enabled: false,
            default_group: false
          },
          rerouted_users: 3,
          snapshot_generation: "policy-snapshot"
        }
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "停用账号" }));
    await screen.findByText("停用 alpha");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("现有用户切换到")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "确认停用" }));

    expect(await screen.findByText("CPA 账号选择策略已更新")).toBeInTheDocument();
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/accounts?window=today")).toHaveLength(2));
    const mutation = requestsTo(fetchMock, "/admin/api/accounts/policy")[0];
    expect(mutation?.[1]).toMatchObject({
      method: "POST",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(JSON.parse(String(mutation?.[1]?.body))).toEqual({
      id: "alpha",
      group_enabled: false,
      default_group: false,
      fallback_account: "beta"
    });
  });

  it("preserves the chosen disable fallback when the account catalog refreshes", async () => {
    const expanded = { ...catalog, accounts: [...catalog.accounts, { ...catalog.accounts[1], id: "gamma" }] };
    vi.stubGlobal("fetch", accountPageFetchMock(expanded));
    const user = userEvent.setup();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<MemoryRouter><QueryClientProvider client={queryClient}><AccountsPage csrfToken="csrf-test" /></QueryClientProvider></MemoryRouter>);
    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "停用账号" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(dialog.querySelector("select")!, { target: { value: "gamma" } });
    queryClient.setQueryData(["accounts", "today", null, null], {
      ...expanded, accounts: expanded.accounts.map(account => ({ ...account, routed_users: account.routed_users + 1 }))
    });
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "现有用户切换到：gamma" })).toBeInTheDocument());
  });

  it("checks legacy OAuth jobs before confirming and submits through the legacy operation path", async () => {
    const clipboard = { writeText: vi.fn(async () => undefined) };
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/jobs": { jobs: [] },
      "/admin/api/operations": {
        message: "任务已提交",
        reused: false,
        job: legacyJob({
          id: "oauth-new", name: "OAuth 授权", target: "alpha", status: "succeeded",
          finished_at: 101, exit_code: 0,
          output: ["Codex device URL: https://auth.example.test/device", "Codex device code: TEST-CODE"]
        })
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", { value: clipboard, configurable: true });
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "重新 OAuth" }));
    expect(await screen.findByText("开始 OAuth 授权？")).toBeInTheDocument();
    expect(requestsTo(fetchMock, "/admin/api/jobs")).toHaveLength(1);
    expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "开始授权" }));

    expect(await screen.findByText("TASK OUTPUT")).toBeInTheDocument();
    expect(screen.getByText("TEST-CODE")).toBeInTheDocument();
    const taskMeta = document.querySelector<HTMLElement>(".oauth-task-meta");
    expect(taskMeta).not.toBeNull();
    expect(within(taskMeta!).getByText("alpha@example.com")).toBeInTheDocument();
    const authorizationPanel = screen.getByLabelText("OAuth 设备授权信息");
    expect(within(authorizationPanel).queryByText("设备授权")).not.toBeInTheDocument();
    expect(within(authorizationPanel).queryByRole("button", { name: "打开授权页" })).not.toBeInTheDocument();
    const addressCard = within(authorizationPanel).getByText("授权地址").closest<HTMLElement>("div");
    const codeCard = within(authorizationPanel).getByText("设备码").closest<HTMLElement>("div");
    expect(addressCard).not.toBeNull();
    expect(codeCard).not.toBeNull();
    await user.click(within(addressCard!).getByRole("button", { name: "复制地址" }));
    expect(await within(addressCard!).findByRole("button", { name: "已复制" })).toBeInTheDocument();
    await user.click(within(addressCard!).getByRole("button", { name: "已复制" }));
    expect(clipboard.writeText).toHaveBeenNthCalledWith(1, "https://auth.example.test/device");
    expect(clipboard.writeText).toHaveBeenNthCalledWith(2, "https://auth.example.test/device");
    await user.click(within(codeCard!).getByRole("button", { name: "复制设备码" }));
    expect(await within(codeCard!).findByRole("button", { name: "已复制" })).toBeInTheDocument();
    expect(clipboard.writeText).toHaveBeenNthCalledWith(3, "TEST-CODE");
    expect(screen.queryByText("设备码已复制")).not.toBeInTheDocument();
    const mutation = requestsTo(fetchMock, "/admin/api/operations")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({ action: "login", target: "alpha" });
  });

  it("opens an existing legacy OAuth job without creating a duplicate", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/jobs": {
        jobs: [legacyJob({
          id: "oauth-existing", name: "OAuth 授权", target: "alpha", status: "running",
          started_at: 101, output: ["waiting for device authorization"]
        })]
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "重新 OAuth" }));

    expect(await screen.findByText("该账号已有 OAuth 授权任务，已直接打开")).toBeInTheDocument();
    expect(screen.getByText("waiting for device authorization")).toBeInTheDocument();
    expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(0);
  });

  it("creates an account through the write-only lifecycle API and refreshes the catalog", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts": {
        message: "业务 CPA 已创建并通过运行探针",
        account: {
          account: {
            id: "gamma",
            email: "gamma@example.com",
            port: 18321,
            proxy_mode: "inherit",
            created_at: 101,
            group_enabled: true,
            default_group: false
          },
          created_key_rows: 4,
          snapshot_generation: "new-snapshot"
        }
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "添加 CPA" }));
    const createDialog = await screen.findByRole("dialog");
    expect(createDialog).toHaveTextContent("添加业务 CPA");
    expect(createDialog).toHaveTextContent("NEW BUSINESS CPA");
    expect(createDialog).toHaveTextContent("后台补齐已有用户 Key");
    const accountID = screen.getByLabelText("账号标识");
    const accountEmail = screen.getByLabelText("上游账号邮箱");
    await user.type(accountID, "gamma");
    await user.tab();
    expect(accountEmail).toHaveFocus();
    await user.type(accountEmail, "Gamma@Example.com");
    await user.tab();
    expect(within(createDialog).getByRole("button", { name: /出口代理/ })).toHaveFocus();
    await user.tab();
    expect(within(createDialog).getByRole("button", { name: "创建并启动" })).toHaveFocus();
    await user.keyboard("{Enter}");

    expect(await screen.findByText("业务 CPA 已创建并通过运行探针")).toBeInTheDocument();
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/accounts?window=today")).toHaveLength(2));
    const mutation = requestsTo(fetchMock, "/admin/api/accounts")[0];
    expect(mutation).toBeDefined();
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({
      id: "gamma",
      email: "Gamma@Example.com",
      proxy_mode: "inherit",
      proxy_url: ""
    });
  });

  it("uses the legacy second confirmation for rename and preserves the editor on cancel", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts/update": {
        message: "CPA 已重命名、重建并通过运行探针",
        account: {
          account: {
            id: "gamma", email: "alpha@example.com", port: 18319, proxy_mode: "inherit",
            created_at: 100, group_enabled: true, default_group: true
          },
          renamed_from: "alpha", rerouted_users: 0, snapshot_generation: "renamed-snapshot"
        }
      }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "编辑 alpha" }));
    expect(screen.queryByLabelText("输入 alpha 确认重命名")).not.toBeInTheDocument();
    await user.clear(screen.getByLabelText("CPA 标识"));
    await user.type(screen.getByLabelText("CPA 标识"), "gamma");
    await user.click(screen.getByRole("button", { name: "保存修改" }));

    expect(await screen.findByText("修改业务 CPA？")).toBeInTheDocument();
    expect(screen.getByText("alpha 将迁移为 gamma。相关容器会短暂重启，OAuth、日志和 Key 关联会保留。")).toBeInTheDocument();
    const renameConfirmation = screen.getAllByRole("dialog").at(-1)!;
    await user.click(within(renameConfirmation).getByRole("button", { name: /取\s*消/ }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "修改业务 CPA？" })).not.toBeInTheDocument());
    expect(screen.getByLabelText("CPA 标识")).toHaveValue("gamma");
    expect(requestsTo(fetchMock, "/admin/api/accounts/update")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "保存修改" }));
    expect(await screen.findByText("修改业务 CPA？")).toBeInTheDocument();
    const secondRenameConfirmation = screen.getAllByRole("dialog").at(-1)!;
    await user.click(within(secondRenameConfirmation).getByRole("button", { name: "确认修改" }));
    expect(await screen.findByText("CPA 已重命名、重建并通过运行探针")).toBeInTheDocument();
    const mutation = requestsTo(fetchMock, "/admin/api/accounts/update")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({
      id: "alpha",
      new_id: "gamma",
      email: "alpha@example.com",
      proxy_mode: "inherit",
      proxy_url: "",
      confirm: "alpha"
    });
  });

  it("never echoes an encrypted proxy and only submits an explicitly replaced value", async () => {
    const proxyCatalog = {
      ...catalog,
      accounts: catalog.accounts.map((account) => account.id === "alpha"
        ? { ...account, proxy_mode: "custom", proxy_configured: true }
        : account)
    };
    const fetchMock = accountPageFetchMock(proxyCatalog, { "/admin/api/accounts/update": {
        message: "CPA 账号已更新并通过运行探针",
        account: {
          account: {
            id: "alpha",
            email: "alpha@example.com",
            port: 18319,
            proxy_mode: "custom",
            created_at: 100,
            group_enabled: true,
            default_group: true
          },
          rerouted_users: 0,
          snapshot_generation: "updated-snapshot"
        }
      } });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    await user.click(screen.getByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "编辑 alpha" }));
    await waitFor(() => expect(screen.getByLabelText("CPA 标识")).toHaveFocus());
    const proxyInput = screen.getByLabelText("账号代理 URL");
    expect(proxyInput.parentElement?.querySelector('[role="button"]')).toHaveAttribute("tabindex", "-1");
    expect(proxyInput).toHaveValue("");
    expect(proxyInput).toHaveAttribute("placeholder", "留空保持现有代理；支持 HTTP、HTTPS、SOCKS5");
    expect(screen.queryByText("清除已加密保存的独立代理地址")).not.toBeInTheDocument();
    await user.type(proxyInput, "socks5://user:password@127.0.0.1:1080");
    expect(screen.getByLabelText("CPA 标识")).toHaveValue("alpha");
    expect(proxyInput).toHaveValue("socks5://user:password@127.0.0.1:1080");
    await user.click(screen.getByRole("button", { name: "保存修改" }));
    expect(await screen.findByText("修改出口代理？")).toBeInTheDocument();
    expect(screen.getByText("出口代理设置将更新。相关容器会短暂重启，OAuth、日志和 Key 关联会保留。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认修改" }));

    expect(await screen.findByText("CPA 账号已更新并通过运行探针")).toBeInTheDocument();
    const mutation = fetchMock.mock.calls.find(([url]) => String(url) === "/admin/api/accounts/update");
    expect(mutation).toBeDefined();
    const mutationBody = mutation?.[1]?.body;
    expect(typeof mutationBody).toBe("string");
    const payload = JSON.parse(String(mutationBody));
    expect(payload.proxy_url).toBe("socks5://user:password@127.0.0.1:1080");
    expect(payload).toMatchObject({ id: "alpha", new_id: "alpha", proxy_mode: "custom" });
  });

  it("defaults the legacy fallback and submits the exact delete contract", async () => {
    const fetchMock = accountPageFetchMock(catalog, { "/admin/api/accounts/delete": {
        message: "业务 CPA 已删除，配置、授权和日志已安全归档",
        account: {
          account_id: "alpha",
          removed_key_rows: 4,
          revoked_exclusive_keys: 0,
          rerouted_users: 3,
          replacement_account: "beta",
          backup: "state/backups/account-alpha",
          snapshot_generation: "delete-snapshot"
        }
      } });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    await user.click(screen.getByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "编辑 alpha" }));
    await user.click(await screen.findByRole("button", { name: "删除 CPA" }));
    expect(await screen.findByText("删除业务 CPA")).toBeInTheDocument();
    expect(screen.getByText("DESTRUCTIVE ACTION")).toBeInTheDocument();
    expect(screen.queryByText("同时停用该 CPA 仍然独占的 Key")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "用户切换到：beta" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("输入 CPA 标识以确认"), "alpha");
    const deleteDialog = screen.getAllByRole("dialog").at(-1)!;
    await user.click(within(deleteDialog).getByRole("button", { name: "确认删除" }));

    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/accounts/delete")).toHaveLength(1));
    expect(await screen.findByText("业务 CPA 已删除，配置、授权和日志已安全归档")).toBeInTheDocument();
    const mutation = fetchMock.mock.calls.find(([url]) => String(url) === "/admin/api/accounts/delete");
    expect(mutation).toBeDefined();
    const mutationBody = mutation?.[1]?.body;
    expect(typeof mutationBody).toBe("string");
    expect(JSON.parse(String(mutationBody))).toEqual({
      id: "alpha",
      confirm: "alpha",
      revoke_keys: false,
      fallback_account: "beta"
    });
  });

  it("matches the legacy delete flow by sending a non-empty mismatched confirmation to the server", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts/delete": new Response(JSON.stringify({
        error: { code: "invalid_confirmation", message: "确认内容必须与 CPA 标识完全一致" }
      }), { status: 400, headers: { "Content-Type": "application/json" } })
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("row", { name: "展开 alpha" }));
    await user.click(screen.getByRole("button", { name: "编辑 alpha" }));
    await user.click(await screen.findByRole("button", { name: "删除 CPA" }));
    await user.type(screen.getByLabelText("输入 CPA 标识以确认"), "not-alpha");
    const deleteDialog = screen.getAllByRole("dialog").at(-1)!;
    await user.click(within(deleteDialog).getByRole("button", { name: "确认删除" }));

    expect(await within(deleteDialog).findByText("确认内容必须与 CPA 标识完全一致")).toBeInTheDocument();
    expect(within(deleteDialog).getByText("删除业务 CPA")).toBeInTheDocument();
    const mutation = requestsTo(fetchMock, "/admin/api/accounts/delete")[0];
    expect(JSON.parse(String(mutation[1]?.body))).toMatchObject({
      id: "alpha",
      confirm: "not-alpha",
      fallback_account: "beta"
    });
  });

  it("filters accounts by search, runtime state, and OAuth status", async () => {
    vi.stubGlobal("fetch", accountPageFetchMock(catalog));
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("搜索 CPA 账号"), "beta");
    expect(screen.queryByText("alpha", { selector: ".account-name-cell .table-primary" })).not.toBeInTheDocument();
    expect(screen.getByText("beta", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();

    await user.clear(screen.getByLabelText("搜索 CPA 账号"));
    await user.click(screen.getByRole("combobox", { name: "运行状态" }));
    await clickVisibleOption(user, "已停止");
    expect(screen.queryByText("alpha", { selector: ".account-name-cell .table-primary" })).not.toBeInTheDocument();
    expect(screen.getByText("beta", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: "运行状态" }));
    const allLabels = await screen.findAllByText("全部");
    const allOption = allLabels.find((element) => element.classList.contains("ant-select-item-option-content"));
    expect(allOption).toBeDefined();
    await user.click(allOption!);
    await user.click(screen.getByRole("combobox", { name: "OAuth" }));
    const pendingLabels = await screen.findAllByText("待授权");
    const pendingOption = pendingLabels.find((element) => element.classList.contains("ant-select-item-option-content"));
    expect(pendingOption).toBeDefined();
    await user.click(pendingOption!);
    expect(screen.queryByText("alpha", { selector: ".account-name-cell .table-primary" })).not.toBeInTheDocument();
    expect(screen.getByText("beta", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
  });

  it("fails closed for a previous candidate payload without reset and operational fields", async () => {
    const previousCandidateCatalog = structuredClone(catalog) as typeof catalog;
    for (const account of previousCandidateCatalog.accounts) {
      const mutable = account as unknown as Record<string, unknown>;
      delete mutable.reset_credit_count;
      delete mutable.resettable;
      delete mutable.reset_window_labels;
      delete mutable.operational_status;
    }
    previousCandidateCatalog.accounts[1].account_state.reason = "container_not_running";
    vi.stubGlobal("fetch", accountPageFetchMock(previousCandidateCatalog));
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    expect(screen.queryByText(/剩余 undefined 次/)).not.toBeInTheDocument();
    expect(screen.getAllByText("额度未知")).toHaveLength(2);
    expect(screen.getByText("已停止", { selector: ".account-runtime-status" })).toHaveAttribute(
      "aria-label",
      "已停止：CPA 容器未运行"
    );
  });

  it("renders account status details in a body-level tooltip instead of inside the clipped table", async () => {
    vi.stubGlobal("fetch", accountPageFetchMock(catalog));
    const user = userEvent.setup();
    renderPage();

    const status = await screen.findByText("已停止", { selector: ".account-runtime-status" });
    await user.hover(status);
    const tooltip = await screen.findByText("CPA 容器未运行", { selector: ".ant-tooltip-container" });
    expect(tooltip).toHaveTextContent("CPA 容器未运行");
    expect(tooltip.closest(".account-legacy-table")).toBeNull();
  });

  it("requests a fresh account catalog when the usage window changes", async () => {
    const fetchMock = accountPageFetchMock(catalog, {
      "/admin/api/accounts?window=2592000": { ...catalog, window: "2592000", window_seconds: 2592000 }
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    expect(requestsTo(fetchMock, "/admin/api/accounts?window=today")).toHaveLength(1);
    await user.click(await screen.findByText("30 天"));
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/accounts?window=2592000")).toHaveLength(1));
  });

  it("keeps the previous range on cancel and applies one custom range to the list and expanded detail", async () => {
    const fetchMock = accountPageFetchMock(catalog);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha", { selector: ".account-name-cell .table-primary" })).toBeInTheDocument();
    await user.click(await screen.findByText("额度周期"));
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/accounts?window=since_reset")).toHaveLength(1));

    await user.click(screen.getByRole("row", { name: "展开 alpha" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => {
      const url = new URL(String(input), "http://preview.test");
      return url.pathname === "/admin/api/accounts/usage-breakdown"
        && url.searchParams.get("account") === "alpha"
        && url.searchParams.get("window") === "since_reset";
    })).toBe(true));

    await user.click(screen.getByRole("button", { name: "时间选择" }));
    expect(await screen.findByText("账号信息自定义统计范围")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.queryByText("CUSTOM USAGE RANGE")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /取\s*消/ }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(requestsMatching(fetchMock, "/admin/api/accounts", "custom")).toHaveLength(0);
    expect(screen.getByRole("button", { name: "额度周期" })).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("button", { name: "时间选择" }));
    await user.click(await screen.findByRole("button", { name: "应用范围" }));

    await waitFor(() => expect(requestsMatching(fetchMock, "/admin/api/accounts", "custom")).toHaveLength(1));
    const listRequest = new URL(String(requestsMatching(fetchMock, "/admin/api/accounts", "custom")[0][0]), "http://preview.test");
    const startAt = listRequest.searchParams.get("start_at");
    const endAt = listRequest.searchParams.get("end_at");
    expect(Number(startAt)).toBeLessThan(Number(endAt));
    expect(Number(endAt)).toBeLessThanOrEqual(Math.floor(Date.now() / 1000));

    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => {
      const url = new URL(String(input), "http://preview.test");
      return url.pathname === "/admin/api/accounts/usage-breakdown"
        && url.searchParams.get("account") === "alpha"
        && url.searchParams.get("window") === "custom"
        && url.searchParams.get("start_at") === startAt
        && url.searchParams.get("end_at") === endAt;
    })).toBe(true));
  }, 10_000);
});

function renderPage(entry = "/accounts") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={queryClient}>
        <AccountsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function legacyJob(overrides: Record<string, unknown>) {
  return {
    id: "job-1",
    name: "运行任务",
    target: "all",
    status: "queued",
    created_at: 100,
    started_at: null,
    finished_at: null,
    exit_code: null,
    output: [],
    ...overrides
  };
}

function accountPageFetchMock(catalogPayload: typeof catalog, exactResponses: Record<string, unknown> = {}) {
  return vi.fn((input: RequestInfo | URL, _init?: RequestInit) => {
    const url = String(input);
    if (Object.hasOwn(exactResponses, url)) {
      const response = exactResponses[url];
      return Promise.resolve(response instanceof Response ? response : jsonResponse(response));
    }
    if (url.startsWith("/admin/api/accounts?")) return Promise.resolve(jsonResponse(catalogPayload));
    if (url.startsWith("/admin/api/accounts/usage-breakdown?")) return Promise.resolve(jsonResponse(accountBreakdown));
    if (url === "/admin/api/images/cliproxy") return Promise.resolve(jsonResponse(accountImages));
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
}

function requestsTo(fetchMock: ReturnType<typeof accountPageFetchMock>, url: string) {
  return fetchMock.mock.calls.filter(([input]) => String(input) === url);
}

function requestsMatching(
  fetchMock: ReturnType<typeof accountPageFetchMock>,
  pathname: string,
  window: string
) {
  return fetchMock.mock.calls.filter(([input]) => {
    const url = new URL(String(input), "http://preview.test");
    return url.pathname === pathname && url.searchParams.get("window") === window;
  });
}

async function clickVisibleOption(user: ReturnType<typeof userEvent.setup>, label: string) {
  const options = await screen.findAllByText(label, { exact: true });
  const visibleItem = options.map((option) => option.closest<HTMLElement>(".ant-select-item-option")).find((item) => {
    if (!item || item.classList.contains("ant-select-item-option-disabled")) return false;
    const popup = item.closest<HTMLElement>(".ant-select-dropdown");
    return popup && !popup.classList.contains("ant-select-dropdown-hidden") && popup.style.display !== "none";
  });
  expect(visibleItem).toBeDefined();
  await user.click(visibleItem!);
}
