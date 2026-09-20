import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { UsersPage } from "./UsersPage";

describe("UsersPage legacy parity", () => {
  it.each([
    { action: "轮换唯一 Key", dialog: "轮换 Key？" },
    { action: "停用唯一 Key", dialog: "停用用户的 API Key？" },
    { action: "删除用户", dialog: "删除用户与 API Key？" }
  ])("groups user actions and preserves confirmation for $action", async ({ action, dialog }) => {
    const fetchMock = userFetchMock();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();
    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    const actions = await screen.findByRole("group", { name: "用户管理操作" });
    expect(Array.from(actions.querySelectorAll("button"), (button) => button.textContent?.trim())).toEqual([
      "设置团队", "配置周额度", "重置密码", "更多操作"
    ]);
    const more = within(actions).getByRole("button", { name: "更多操作" });
    expect(more).toHaveAttribute("aria-expanded", "false");
    expect(document.querySelector(".user-detail-actions-menu")).not.toBeInTheDocument();
    await user.click(more);
    expect(more).toHaveAttribute("aria-expanded", "true");
    const menu = await screen.findByRole("menu", { name: "更多用户操作" });
    expect(within(menu).getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "轮换唯一 Key", "停用唯一 Key", "删除用户"
    ]);
    await user.click(within(menu).getByRole("menuitem", { name: action }));
    const confirmation = await screen.findByRole("dialog");
    expect(within(confirmation).getByRole("heading", { name: dialog })).toBeInTheDocument();
    expect(more).toHaveAttribute("aria-expanded", "false");
    expect(fetchMock.mock.calls.some(([, init]) => init?.method && init.method !== "GET")).toBe(false);
  });

  it("hides key lifecycle menu items when the user has no active key", async () => {
    vi.stubGlobal("fetch", userFetchMock((path) => {
      if (!path.startsWith("/admin/api/users?")) return undefined;
      return Promise.resolve(jsonResponse({ ...userCatalog(), users: [{ ...baseUser(), active_keys: 0 }] }));
    }));
    const user = userEvent.setup();
    renderUsers();
    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    const menu = await screen.findByRole("menu", { name: "更多用户操作" });
    expect(within(menu).queryByRole("menuitem", { name: "轮换唯一 Key" })).not.toBeInTheDocument();
    expect(within(menu).queryByRole("menuitem", { name: "停用唯一 Key" })).not.toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "删除用户" })).toBeInTheDocument();
  });

  it("keeps token values inline and presents effort tooltip metrics as label-value pairs", async () => {
    vi.stubGlobal("fetch", userFetchMock());
    const user = userEvent.setup();
    renderUsers();
    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    const table = await screen.findByLabelText("模型用量表格");
    const token = table.querySelector(".token-usage") as HTMLElement;
    expect(token.querySelector(".token-usage-exact")).toHaveTextContent("1,200 Token");
    expect(token.querySelector(".token-usage-sr-only")).toHaveTextContent("1,200 Token");
    await user.hover(token);
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 200)); });
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    await user.unhover(token);

    const effort = within(table).getByRole("button", { name: "查看 gpt-5.6 · high 的 CPA 用量分布" });
    await user.hover(effort);
    const tooltip = await screen.findByRole("tooltip", { name: "gpt-5.6 · high" });
    expect(tooltip.querySelectorAll("dt")).toHaveLength(7);
    expect(within(tooltip).getByText("调用", { selector: "dt" }).nextElementSibling).toHaveTextContent("4");
    expect(within(tooltip).getByText("总 Token", { selector: "dt" }).nextElementSibling).toHaveTextContent("1,200");
    expect(within(tooltip).getByText("加权 Token", { selector: "dt" }).nextElementSibling).toHaveTextContent("1,500");
    await user.unhover(effort);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  it("opens the create dialog from the first-run deep link", async () => {
    vi.stubGlobal("fetch", userFetchMock());
    renderUsers("/users?create=1");
    const dialog = await screen.findByRole("dialog", { name: /添加用户/ });
    const notice = dialog.querySelector(".inline-notice");
    expect(notice).toHaveTextContent("系统会创建统一 API Key，并为用户设置系统默认初始密码。");
    expect(notice).toHaveTextContent("API Key 只显示一次；");
    expect(notice).toHaveTextContent("用户首次登录必须修改默认密码。");
    expect(notice?.querySelectorAll("br")).toHaveLength(2);
  });

  it("keeps pagination in the table panel but outside its scroll viewport", async () => {
    vi.stubGlobal("fetch", userFetchMock());
    const user = userEvent.setup();
    renderUsers();

    const email = await screen.findByText("alice@example.com");
    const panel = email.closest(".legacy-user-table-state");
    const pagination = screen.getByLabelText("用户分页");
    expect(panel).not.toBeNull();
    expect(pagination.parentElement).toBe(panel);
    expect(pagination.closest(".admin-table-viewport")).toBeNull();
    expect(within(pagination).getByRole("button", { name: "上一页" })).toBeDisabled();
    expect(within(pagination).getByRole("button", { name: "下一页" })).toBeDisabled();

    await user.click(within(pagination).getByRole("button", { name: "每页条数：50" }));
    const options = screen.getByRole("listbox", { name: "每页条数" });
    expect(options.parentElement).toBe(document.body);
    await user.click(within(options).getByRole("option", { name: "25" }));
    expect(within(pagination).getByRole("button", { name: "每页条数：25" })).toBeInTheDocument();
  });

  it("loads the catalog, expands user detail and keeps team filtering without team shortcuts", async () => {
    const fetchMock = userFetchMock();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /管理团队/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /团队用量|Token 用量$/ })).not.toBeInTheDocument();
    expect(screen.getByText("Platform", { selector: ".team-chip" })).toHaveClass("team-tag-style-rose");
    await waitFor(() => expect(requestPaths(fetchMock)).toContain("/admin/api/teams/usage?window=today"));
    expect(screen.getAllByRole("columnheader")).toHaveLength(11);
    expect(screen.getByRole("button", { name: /Token 用量，当前降序/ })).toBeInTheDocument();
    await user.type(screen.getByRole("searchbox", { name: "搜索用户" }), "alice{Enter}");
    await waitFor(() => expect(requestPaths(fetchMock).some((path) => path.includes("q=alice"))).toBe(true));
    expect(requestPaths(fetchMock).some((path) => path.includes("/users/detail"))).toBe(false);
    expect(requestPaths(fetchMock).some((path) => path.includes("/users/usage-breakdown"))).toBe(false);
    expect(requestPaths(fetchMock).some((path) => path.includes("/teams/usage-breakdown"))).toBe(false);

    const row = screen.getByText("alice@example.com").closest("tr");
    expect(row).not.toBeNull();
    await user.click(row!);
    expect(await screen.findByText("模型与推理分析")).toBeInTheDocument();
    expect(await screen.findByText("alpha", { selector: ".user-account-table .table-primary" })).toBeInTheDocument();
    const accountTable = screen.getByLabelText("用户账号明细表格");
    expect(accountTable.previousElementSibling).toHaveTextContent("CPA 账号用量分析");
    await waitFor(() => expect(screen.getByLabelText("模型用量表格")).toBeInTheDocument());
    expect(screen.queryByLabelText("推理强度用量明细表格")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "CPA：全部 CPA" }).closest(".usage-analysis-header")).toHaveTextContent("模型与推理分析");
    await waitFor(() => {
      expect(requestPaths(fetchMock).some((path) => path.startsWith("/admin/api/users/detail?"))).toBe(true);
      expect(requestPaths(fetchMock).some((path) => path.startsWith("/admin/api/users/usage-breakdown?"))).toBe(true);
    });

    await user.click(screen.getByRole("button", { name: "团队：全部团队" }));
    await user.click(screen.getByRole("option", { name: "Platform" }));
    await waitFor(() => expect(requestPaths(fetchMock).some((path) => (
      path.startsWith("/admin/api/users?") && new URL(path, "http://localhost").searchParams.get("team_id") === "team_platform"
    ))).toBe(true));
    expect(screen.getByRole("button", { name: "团队：Platform" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /管理团队/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /团队用量|Token 用量$/ })).not.toBeInTheDocument();
    expect(requestPaths(fetchMock).some((path) => path.startsWith("/admin/api/teams/usage-breakdown?"))).toBe(false);
  }, 15_000);

  it.each([
    { success: 3, failed: 1 },
    { success: 3, failed: 0 },
    { success: 0, failed: 0 },
    { success: 0, failed: 2 }
  ])("shows $success successful and $failed failed calls in the summary instead of the analysis header", async ({ success, failed }) => {
    const fetchMock = userFetchMock((path) => {
      if (!path.startsWith("/admin/api/users/usage-breakdown?")) return undefined;
      const breakdown = usageBreakdown();
      return Promise.resolve(jsonResponse({
        ...breakdown,
        totals: { ...breakdown.totals, request_count: success + failed, success_count: success, failed_count: failed },
        combinations: success ? breakdown.combinations : []
      }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    const header = (await screen.findByText("模型与推理分析")).closest(".usage-analysis-header") as HTMLElement;
    const calls = (await screen.findByText("成功调用")).closest(".usage-analysis-call-stat") as HTMLElement;
    expect(calls.parentElement).toHaveClass("usage-analysis-summary");
    expect(header.nextElementSibling).toBe(calls.parentElement);
    expect(calls.parentElement?.nextElementSibling).toHaveClass(success ? "usage-model-table-wrap" : "usage-analysis-message");
    expect(within(calls).getByText("成功调用").nextElementSibling).toHaveTextContent(String(success));
    expect(within(calls).getByText("失败调用").nextElementSibling).toHaveTextContent(String(failed));
    expect(within(calls).getByText("失败调用").nextElementSibling).toHaveClass("usage-analysis-failed-count");
    expect(within(calls).getByText("成功调用").nextElementSibling).not.toHaveClass("usage-analysis-failed-count");
    expect(screen.getAllByText("失败调用", { exact: true })).toHaveLength(1);
    expect(within(header).queryByText("成功调用")).not.toBeInTheDocument();
    expect(within(header).queryByText("失败调用")).not.toBeInTheDocument();
    expect(within(header).queryByText("CPA", { exact: true })).not.toBeInTheDocument();
    expect(within(header).getByRole("button", { name: "CPA：全部 CPA" })).toBeInTheDocument();
    const breakdownRequests = requestPaths(fetchMock).filter((path) => path.startsWith("/admin/api/users/usage-breakdown?"));
    expect(breakdownRequests.length).toBeGreaterThan(0);
    expect(breakdownRequests.every((path) => !new URL(path, "http://localhost").searchParams.has("account"))).toBe(true);
  });

  it("keeps the summary and CPA account table when the selected time range has no successful calls", async () => {
    const emptyUsage = {
      request_count: 0, success_count: 0, failed_count: 0,
      input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, cached_tokens: 0,
      total_tokens: 0, weighted_tokens: 0, known_effort_count: 0, last_used_at: null
    };
    const fetchMock = userFetchMock((path) => {
      if (path.startsWith("/admin/api/users/usage-breakdown?")) {
        return Promise.resolve(jsonResponse({ ...usageBreakdown(), totals: emptyUsage, combinations: [] }));
      }
      return undefined;
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    const analysis = (await screen.findByText("模型与推理分析")).closest(".user-usage-analysis") as HTMLElement;
    expect(await within(analysis).findByText("当前范围暂无成功调用")).toBeInTheDocument();
    expect(analysis.querySelector(".usage-model-table")).not.toBeInTheDocument();
    const summary = analysis.querySelector(".usage-analysis-summary") as HTMLElement;
    expect(summary.parentElement).toBe(analysis);
    expect(summary.children).toHaveLength(5);
    expect(within(summary).getByText("成功调用").nextElementSibling).toHaveTextContent("0");
    expect(within(summary).getByText("失败调用").nextElementSibling).toHaveTextContent("0");
    expect(within(analysis).queryByRole("alert")).not.toBeInTheDocument();

    expect(within(analysis).getByRole("button", { name: "CPA：全部 CPA" })).toBeInTheDocument();
    expect(screen.queryByLabelText("推理强度用量明细表格")).not.toBeInTheDocument();
    expect(screen.getByText("CPA 账号用量分析")).toBeInTheDocument();
    expect(screen.getByLabelText("用户账号明细表格")).toBeInTheDocument();
  });

  it("opens the selected model and effort CPA distribution in a drawer without changing the main summary", async () => {
    const fetchMock = userFetchMock((path) => {
      if (!path.startsWith("/admin/api/users/usage-breakdown?")) return undefined;
      return Promise.resolve(jsonResponse({
        ...usageBreakdown(),
        combinations: [
          { ...metrics(1_200, 1_800), account: "alpha", model: "gpt-5.6", reasoning_effort: "high", success_count: 3 },
          { ...metrics(2_400, 3_600), account: "beta", model: "gpt-5.6", reasoning_effort: "high", success_count: 1 },
          { ...metrics(9_000, 9_000), account: "gamma", model: "gpt-5.4", reasoning_effort: "high" },
          { ...metrics(3_000, 3_000), account: "delta", model: "gpt-5.6", reasoning_effort: "low" }
        ]
      }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();
    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    const trigger = await screen.findByRole("button", { name: "查看 gpt-5.6 · high 的 CPA 用量分布" });
    const summary = document.querySelector(".usage-analysis-summary") as HTMLElement;
    const initialSummary = summary.textContent;
    const initialRequests = requestPaths(fetchMock).filter((path) => path.startsWith("/admin/api/users/usage-breakdown?"));
    await user.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "gpt-5.6 · high · CPA 用量分布" });
    expect(within(dialog).getByText("high", { selector: ".user-model-effort-tag" })).toHaveClass("account-model-effort-high");
    const alpha = within(dialog).getByText("alpha", { selector: ".table-primary" }).closest("tr")!;
    const beta = within(dialog).getByText("beta", { selector: ".table-primary" }).closest("tr")!;
    expect(alpha.querySelectorAll("td.user-model-account-number")).toHaveLength(3);
    const rawHeader = within(dialog).getByRole("columnheader", { name: "未加权 Token" });
    const accountOrder = () => Array.from(dialog.querySelectorAll(".ant-table-tbody .table-primary"), (cell) => cell.textContent);
    expect(rawHeader).toHaveAttribute("aria-sort", "descending");
    expect(rawHeader.querySelector(".user-model-account-sort-arrow.active")).toHaveTextContent("↓");
    expect(accountOrder()).toEqual(["beta", "alpha"]);
    await user.click(rawHeader);
    expect(rawHeader).toHaveAttribute("aria-sort", "ascending");
    expect(rawHeader.querySelector(".user-model-account-sort-arrow.active")).toHaveTextContent("↑");
    expect(accountOrder()).toEqual(["alpha", "beta"]);
    await user.click(rawHeader);
    expect(rawHeader).toHaveAttribute("aria-sort", "descending");
    expect(accountOrder()).toEqual(["beta", "alpha"]);
    const callsHeader = within(dialog).getByRole("columnheader", { name: "调用" });
    await user.click(callsHeader);
    expect(callsHeader).toHaveAttribute("aria-sort", "descending");
    expect(rawHeader.querySelector(".user-model-account-sort-arrow")).toHaveTextContent("↕");
    expect(rawHeader.querySelector(".user-model-account-sort-arrow.active")).toBeNull();
    expect(within(dialog).getAllByRole("columnheader").filter((header) => header.querySelector(".user-model-account-sort-arrow.active"))).toHaveLength(1);
    expect(accountOrder()).toEqual(["alpha", "beta"]);
    expect(within(alpha).getByText("75%")).toBeInTheDocument();
    expect(within(beta).getByText("25%")).toBeInTheDocument();
    expect(alpha.children[3]).toHaveTextContent("1,200 Token");
    expect(alpha.children[4]).toHaveTextContent("×1.50");
    expect(alpha.children[5]).toHaveTextContent("1,800 Token");
    expect(alpha.children[6]).toHaveTextContent("400");
    expect(alpha.querySelector("time")).toHaveTextContent("1970/01/01");
    expect(alpha.querySelector("time")).toHaveTextContent("08:04:10");
    expect(within(dialog).queryByText("gamma", { selector: ".table-primary" })).not.toBeInTheDocument();
    expect(within(dialog).queryByText("delta", { selector: ".table-primary" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /CPA：/ })).not.toBeInTheDocument();
    expect(summary.textContent).toBe(initialSummary);
    expect(requestPaths(fetchMock).filter((path) => path.startsWith("/admin/api/users/usage-breakdown?"))).toEqual(initialRequests);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const lowTrigger = screen.getByRole("button", { name: "查看 gpt-5.6 · low 的 CPA 用量分布" });
    lowTrigger.focus();
    await user.keyboard("{Enter}");
    const lowDialog = await screen.findByRole("dialog", { name: "gpt-5.6 · low · CPA 用量分布" });
    expect(within(lowDialog).getByText("low", { selector: ".user-model-effort-tag" })).toHaveClass("account-model-effort-low");
    expect(within(lowDialog).getByText("delta", { selector: ".table-primary" })).toBeInTheDocument();
    expect(within(lowDialog).queryByRole("button", { name: /CPA：/ })).not.toBeInTheDocument();
  });

  it("shares the analysis header CPA filter with model totals and drawer rows, including loading and empty states", async () => {
    const betaUsage = metrics(2_400, 3_600);
    const betaRow = { ...betaUsage, account: "beta", model: "gpt-5.6", reasoning_effort: "high" };
    let finishBeta!: (response: Response) => void;
    const betaResponse = new Promise<Response>((resolve) => { finishBeta = resolve; });
    const fetchMock = userFetchMock((path) => {
      if (path.startsWith("/admin/api/users/detail?")) {
        const detail = userDetail();
        return Promise.resolve(jsonResponse({ ...detail, user: {
          ...detail.user,
          accounts: [detail.user.accounts[0],
            { ...detail.user.accounts[0], account: "beta", usage: betaUsage },
            { ...detail.user.accounts[0], account: "idle" }
          ]
        } }));
      }
      if (!path.startsWith("/admin/api/users/usage-breakdown?")) return undefined;
      const account = new URL(path, "http://localhost").searchParams.get("account");
      if (account === "beta") return betaResponse;
      if (account === "idle") return Promise.resolve(jsonResponse({
        ...usageBreakdown(), account,
        totals: { ...metrics(0, 0), request_count: 0, success_count: 0, failed_count: 0, known_effort_count: 0 },
        combinations: []
      }));
      return Promise.resolve(jsonResponse({
        ...usageBreakdown(),
        totals: { ...metrics(3_600, 5_100), request_count: 8, success_count: 6, failed_count: 2 },
        combinations: [...usageBreakdown().combinations, betaRow]
      }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();
    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    await screen.findByLabelText("模型用量表格");
    const analysis = screen.getByText("模型与推理分析").closest(".user-usage-analysis") as HTMLElement;
    const header = analysis.querySelector(".usage-analysis-header") as HTMLElement;
    const chooseAccount = async (current: string, next: string) => {
      await user.click(within(header).getByRole("button", { name: `CPA：${current}` }));
      await user.click(screen.getByRole("option", { name: next }));
    };
    await chooseAccount("全部 CPA", "beta");
    await waitFor(() => expect(requestPaths(fetchMock).some((path) => path.includes("usage-breakdown?") && path.includes("account=beta"))).toBe(true));
    expect(within(analysis).getByLabelText("正在加载模型分析")).toBeInTheDocument();
    expect(within(analysis).queryByLabelText("模型用量表格")).not.toBeInTheDocument();
    await act(async () => finishBeta(jsonResponse({ ...usageBreakdown(), account: "beta", totals: betaUsage, combinations: [betaRow] })));
    const trigger = await within(analysis).findByRole("button", { name: "查看 gpt-5.6 · high 的 CPA 用量分布" });
    expect(within(analysis).getByText("成功调用").nextElementSibling).toHaveTextContent("3");
    expect(analysis.querySelector(".usage-analysis-token-stat")).toHaveTextContent("2,400 Token");
    await user.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "gpt-5.6 · high · CPA 用量分布" });
    expect(within(dialog).getByText("beta", { selector: ".table-primary" })).toBeInTheDocument();
    expect(within(dialog).queryByText("alpha", { selector: ".table-primary" })).not.toBeInTheDocument();
    expect(within(dialog).getByText("100%")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /CPA：/ })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(within(header).getByRole("button", { name: "CPA：beta" })).toBeInTheDocument();
    expect(document.querySelectorAll(".user-account-table .table-primary")).toHaveLength(3);

    await chooseAccount("beta", "idle");
    expect(await within(analysis).findByText("当前范围暂无成功调用")).toBeInTheDocument();
    expect(within(analysis).getByText("成功调用").nextElementSibling).toHaveTextContent("0");
    await chooseAccount("idle", "全部 CPA");
    expect(await within(analysis).findByLabelText("模型用量表格")).toBeInTheDocument();
    expect(within(analysis).getByText("成功调用").nextElementSibling).toHaveTextContent("6");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("normalizes missing model and effort names and merges their CPA distribution safely", async () => {
    vi.stubGlobal("fetch", userFetchMock((path) => {
      if (!path.startsWith("/admin/api/users/usage-breakdown?")) return undefined;
      return Promise.resolve(jsonResponse({
        ...usageBreakdown(),
        combinations: [
          { ...metrics(600, 600), weighted_tokens: undefined, account: "alpha", model: "unknown", reasoning_effort: "unknown" },
          { ...metrics(600, 900), account: "alpha", model: "", reasoning_effort: "" }
        ]
      }));
    }));
    const user = userEvent.setup();
    renderUsers();
    await user.click((await screen.findByText("alice@example.com")).closest("tr")!);
    await user.click(await screen.findByRole("button", { name: "查看 未上报模型 · 未上报强度 的 CPA 用量分布" }));
    const dialog = await screen.findByRole("dialog", { name: "未上报模型 · 未上报强度 · CPA 用量分布" });
    expect(within(dialog).getByText("未上报强度", { selector: ".user-model-effort-tag" })).toHaveClass("account-model-effort-unknown");
    expect(within(dialog).getAllByText("alpha", { selector: ".table-primary" })).toHaveLength(1);
    const row = within(dialog).getByText("alpha", { selector: ".table-primary" }).closest("tr")!;
    expect(row.children[1]).toHaveTextContent("6");
    expect(row.children[2]).toHaveTextContent("100%");
    expect(row.children[3]).toHaveTextContent("1,200 Token");
    expect(row.children[5]).toHaveTextContent("1,500 Token");
  });

  it("preserves team assignment, create-secret and key-rotation request contracts", async () => {
    const fetchMock = userFetchMock();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "设置 alice@example.com 的团队" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存团队" }));
    expect(await screen.findByText("已更新 1 位用户的团队归属")).toBeInTheDocument();
    const assignment = request(fetchMock, "/admin/api/users/team", "PUT");
    expect(JSON.parse(String(assignment?.[1]?.body))).toEqual({
      email: "alice@example.com",
      team_id: "team_platform"
    });
    expect(new Headers(assignment?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");

    await user.click(screen.getByRole("button", { name: "添加用户" }));
    expect(await screen.findByRole("button", { name: "邮箱后缀：@example.com" })).toBeEnabled();
    expect(screen.getByRole("textbox", { name: "邮箱用户名" })).toHaveAttribute("placeholder", "输入用户名");
    await user.type(screen.getByRole("textbox", { name: "邮箱用户名" }), "new");
    await user.click(screen.getByRole("button", { name: "创建用户" }));
    expect(await screen.findByText("用户已创建")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "用户凭据" })).toBeInTheDocument();
    const keyField = within(screen.getByRole("group", { name: "API Key" }));
    const passwordField = within(screen.getByRole("group", { name: "初始密码" }));
    expect(keyField.getByLabelText("API Key")).toHaveAttribute("type", "password");
    expect(passwordField.getByLabelText("初始密码")).toHaveAttribute("type", "password");
    await user.click(keyField.getByRole("button", { pressed: false }));
    await user.click(passwordField.getByRole("button", { pressed: false }));
    expect(keyField.getByLabelText("API Key")).toHaveAttribute("type", "text");
    expect(keyField.getByLabelText("API Key")).toHaveValue("one-time-api-key");
    expect(passwordField.getByLabelText("初始密码")).toHaveAttribute("type", "text");
    expect(passwordField.getByLabelText("初始密码")).toHaveValue("one-time-password");
    const create = request(fetchMock, "/admin/api/users", "POST");
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({ email: "new@example.com", team_id: null });
    await user.click(screen.getByRole("button", { name: "我已保存" }));
    expect(screen.queryByRole("dialog", { name: "用户凭据" })).not.toBeInTheDocument();

    const row = screen.getByText("alice@example.com").closest("tr");
    await user.click(row!);
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "轮换唯一 Key" }));
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect(await screen.findByText("API Key 已更新")).toBeInTheDocument();
    const rotatedField = within(screen.getByRole("group", { name: "API Key" }));
    expect(rotatedField.getByLabelText("API Key")).toHaveAttribute("type", "password");
    await user.click(rotatedField.getByRole("button", { pressed: false }));
    expect(rotatedField.getByLabelText("API Key")).toHaveAttribute("type", "text");
    expect(rotatedField.getByLabelText("API Key")).toHaveValue("rotated-one-time-key");
    const rotate = request(fetchMock, "/admin/api/keys/rotate", "POST");
    expect(JSON.parse(String(rotate?.[1]?.body))).toEqual({ label: "alice@example.com:alpha" });
    expect(new Headers(rotate?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
  }, 15_000);

  it("hydrates quota from the list, fetches detail on demand and confirms batch restore", async () => {
    let resolveQuota: ((response: Response) => void) | undefined;
    const quotaResponse = new Promise<Response>((resolve) => { resolveQuota = resolve; });
    const fetchMock = userFetchMock((path, init) => {
      if (path === "/admin/api/users/quota?email=alice%40example.com" && (!init?.method || init.method === "GET")) {
        return quotaResponse;
      }
      return undefined;
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "配置" }));
    expect(screen.getByText("当前加权上限")).toBeInTheDocument();
    expect(screen.getByText("基础额度")).toBeInTheDocument();
    expect(requestPaths(fetchMock).filter((path) => path.includes("/users/quota?email="))).toHaveLength(1);
    resolveQuota?.(jsonResponse(quotaResult()));
    expect(await screen.findByText("临时项目扩容")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: /自定义额度/ }));
    const tokenInput = screen.getByRole("spinbutton", { name: "每周 Token" });
    await user.clear(tokenInput);
    await user.type(tokenInput, "500");
    await user.click(screen.getByRole("button", { name: "保存额度策略" }));
    expect(await screen.findByText("用户周额度策略已保存")).toBeInTheDocument();
    const update = request(fetchMock, "/admin/api/users/quota", "PUT");
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({
      email: "alice@example.com",
      mode: "custom",
      weekly_tokens: 500
    });

    // The saved notice can appear before the quota drawer's exit animation ends.
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await user.click(screen.getByRole("checkbox", { name: "选择 alice@example.com" }));
    const selectionBar = screen.getByText("已选择 1 位用户").closest(".user-selection-bar");
    expect(selectionBar).not.toBeNull();
    await user.click(within(selectionBar as HTMLElement).getByRole("button", { name: "恢复组织默认" }));
    expect(screen.getByText("恢复 1 位用户的组织默认额度？")).toBeInTheDocument();
    expect(request(fetchMock, "/admin/api/users/quota-actions", "POST")).toBeUndefined();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "恢复组织默认" }));
    await waitFor(() => expect(request(fetchMock, "/admin/api/users/quota-actions", "POST")).toBeDefined());
    const restore = request(fetchMock, "/admin/api/users/quota-actions", "POST");
    expect(JSON.parse(String(restore?.[1]?.body))).toEqual({
      action: "restore_default",
      scope: "selected",
      users: ["alice@example.com"],
      confirm: "restore_default"
    });
  });
});

describe("create user email suffix selector", () => {
  function withDomains(domains: string[]) {
    return userFetchMock((path) => path === "/site-config.json"
      ? Promise.resolve(jsonResponse({ allowed_email_domains: domains }))
      : undefined);
  }

  it("normalizes configured suffixes, supports keyboard selection and combines the selected address", async () => {
    const fetchMock = withDomains([" @Example.COM ", "example.com", "example.org"]);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers("/users?create=1");
    const input = await screen.findByRole("textbox", { name: "邮箱用户名" });
    await user.type(input, "new");
    await user.tab();
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.com" })).toHaveFocus();
    await user.keyboard("{Enter}");
    const options = screen.getByRole("listbox", { name: "邮箱后缀" });
    expect(within(options).getAllByRole("option")).toHaveLength(2);
    await user.keyboard("{ArrowDown}{Enter}");
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.org" })).toHaveFocus();
    expect(request(fetchMock, "/admin/api/users", "POST")).toBeUndefined();
    await user.click(input);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(request(fetchMock, "/admin/api/users", "POST")).toBeDefined());
    expect(JSON.parse(String(request(fetchMock, "/admin/api/users", "POST")?.[1]?.body))).toEqual({ email: "new@example.org", team_id: null });
  });

  it("splits a pasted full email without duplicating or changing its suffix", async () => {
    const fetchMock = withDomains(["example.com", "example.org"]);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers("/users?create=1");
    const input = await screen.findByRole("textbox", { name: "邮箱用户名" });
    await screen.findByRole("button", { name: "邮箱后缀：@example.com" });
    await user.click(input);
    await user.paste("  new@Example.ORG  ");
    expect(input).toHaveValue("new");
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.org" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "创建用户" }));
    await waitFor(() => expect(request(fetchMock, "/admin/api/users", "POST")).toBeDefined());
    expect(JSON.parse(String(request(fetchMock, "/admin/api/users", "POST")?.[1]?.body)).email).toBe("new@example.org");
  });

  it("accepts a typed full address when configured suffixes share a prefix", async () => {
    const fetchMock = withDomains(["example.com", "example.com.test"]);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers("/users?create=1");
    await user.type(await screen.findByRole("textbox", { name: "邮箱用户名" }), "new@example.com.test");
    await user.click(screen.getByRole("button", { name: "创建用户" }));
    await waitFor(() => expect(request(fetchMock, "/admin/api/users", "POST")).toBeDefined());
    expect(JSON.parse(String(request(fetchMock, "/admin/api/users", "POST")?.[1]?.body)).email).toBe("new@example.com.test");
  });

  it("rejects unconfigured pasted domains instead of silently replacing them", async () => {
    const fetchMock = withDomains(["example.com"]);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers("/users?create=1");
    const input = await screen.findByRole("textbox", { name: "邮箱用户名" });
    await user.click(input);
    await user.paste("new@unconfigured.test");
    await user.click(screen.getByRole("button", { name: "创建用户" }));
    expect(await screen.findByText(/邮箱后缀不匹配/)).toBeInTheDocument();
    expect(input).toHaveValue("new@unconfigured.test");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(request(fetchMock, "/admin/api/users", "POST")).toBeUndefined();
  });

  it("disables creation until suffixes load and preserves the draft when retrying", async () => {
    let finishLoading: ((value: Response) => void) | undefined;
    const pending = new Promise<Response>((resolve) => { finishLoading = resolve; });
    let attempts = 0;
    const fetchMock = userFetchMock((path) => {
      if (path !== "/site-config.json") return undefined;
      return ++attempts === 1 ? pending : Promise.resolve(jsonResponse({ allowed_email_domains: ["example.com"] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers("/users?create=1");
    const input = await screen.findByRole("textbox", { name: "邮箱用户名" });
    await user.type(input, "draft");
    expect(screen.getByRole("button", { name: "创建用户" })).toBeDisabled();
    finishLoading?.(jsonResponse({ error: "site configuration unavailable" }, { status: 503 }));
    await user.click(await screen.findByRole("button", { name: /重\s*试/ }));
    await waitFor(() => expect(screen.getByRole("button", { name: "创建用户" })).toBeEnabled());
    expect(input).toHaveValue("draft");
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.com" })).toBeEnabled();
    expect(request(fetchMock, "/admin/api/users", "POST")).toBeUndefined();
  });

  it("shows the configuration entry and prevents creation when no suffix is configured", async () => {
    const fetchMock = withDomains([]);
    vi.stubGlobal("fetch", fetchMock);
    renderUsers("/users?create=1");
    expect(await screen.findByRole("link", { name: "系统配置" })).toHaveAttribute("href", "/configuration?section=identity&key=identity.allowed_email_domains");
    expect(screen.getByRole("button", { name: "创建用户" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "邮箱后缀：暂无可用后缀" })).toBeDisabled();
    expect(request(fetchMock, "/admin/api/users", "POST")).toBeUndefined();
  });
});

function renderUsers(entry = "/admin/users") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={queryClient}>
        <UsersPage csrfToken="csrf-test" />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

function userFetchMock(override?: (path: string, init?: RequestInit) => Promise<Response> | undefined) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const overridden = override?.(path, init);
    if (overridden) return overridden;
    if (path.startsWith("/admin/api/users?")) return Promise.resolve(jsonResponse(userCatalog()));
    if (path === "/site-config.json") {
      return Promise.resolve(jsonResponse({
        version: 1,
        product_name: "Codex CPA Pool",
        short_name: "CCPA",
        environment_label: "Test",
        public_base_url: "https://cpa.example.com",
        allowed_email_domains: ["example.com"],
        provider_name: "Codex CPA Pool",
        api_key_env: "CCPA_API_KEY",
        default_model: "gpt-5.6-sol",
        logo: {
          custom: false,
          url: "/portal/assets/codex-cpa-pool-logo.svg",
          content_type: "image/svg+xml",
          sha256: "",
          updated_at: null
        }
      }));
    }
    if (path.startsWith("/admin/api/teams/usage-breakdown?")) return Promise.resolve(jsonResponse(teamUsageBreakdown()));
    if (path.startsWith("/admin/api/teams/usage?")) return Promise.resolve(jsonResponse(teamUsageCatalog()));
    if (path.startsWith("/admin/api/users/detail?")) return Promise.resolve(jsonResponse(userDetail()));
    if (path.startsWith("/admin/api/users/usage-breakdown?")) return Promise.resolve(jsonResponse(usageBreakdown()));
    if (path === "/admin/api/users/team" && init?.method === "PUT") {
      return Promise.resolve(jsonResponse({ message: "已更新 1 位用户的团队归属" }));
    }
    if (path === "/admin/api/users" && init?.method === "POST") {
      return Promise.resolve(jsonResponse({
        message: "用户已创建",
        keys: [oneTimeKey("one-time-api-key", "new@example.com")],
        initial_password: "one-time-password",
        team_id: null
      }, { status: 201 }));
    }
    if (path === "/admin/api/keys/rotate" && init?.method === "POST") {
      return Promise.resolve(jsonResponse({ message: "API Key 已轮换", keys: [oneTimeKey("rotated-one-time-key", "alice@example.com")] }));
    }
    if (path === "/admin/api/users/quota?email=alice%40example.com" && (!init?.method || init.method === "GET")) {
      return Promise.resolve(jsonResponse(quotaResult()));
    }
    if (path === "/admin/api/users/quota" && init?.method === "PUT") {
      return Promise.resolve(jsonResponse({ ...quotaResult("custom", 500), message: "用户周额度策略已保存" }));
    }
    if (path === "/admin/api/users/quota-actions" && init?.method === "POST") {
      return Promise.resolve(jsonResponse({
        message: "已恢复所选用户的组织默认额度",
        action: "restore_default",
        scope: "selected",
        requested_users: 1,
        affected_users: 1,
        skipped_users: 0,
        skipped: []
      }));
    }
    return Promise.reject(new Error(`unexpected request: ${path}`));
  });
}

function userCatalog() {
  return {
    users: [baseUser()],
    accounts: { alpha: { email: "alpha@example.com" } },
    teams: [{ id: "team_platform", name: "Platform", description: "Core", tag_style: "rose", user_count: 1, created_at: 100, updated_at: 200 }],
    tags: [],
    collector: { status: "healthy", heartbeat_at: 300, last_success_at: 300, last_error: "", queue_depth: 0 },
    pagination: { page: 1, page_size: 50, total: 1, total_pages: 1 },
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    summary_generated_at: 300,
    summary_cached: false
  };
}

function baseUser() {
  return {
    email: "alice@example.com",
    status: "active",
    active_keys: 1,
    active_accounts: 1,
    total_records: 3,
    created_at: 100,
    updated_at: 200,
    route_account_id: "alpha",
    team_id: "team_platform",
    team: { id: "team_platform", name: "Platform", description: "Core", tag_style: "rose" },
    team_membership_version: 4,
    account_count: 1,
    usage: metrics(1_200, 1_500),
    weekly_quota: weeklyQuota()
  };
}

function userDetail() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    user: {
      ...baseUser(),
      accounts: [{
        account: "alpha",
        account_email: "alpha@example.com",
        status: "active",
        history_count: 3,
        key: { label: "alice@example.com:alpha", account: "alpha", account_email: "alpha@example.com", user: "alice@example.com", status: "active", created_at: 100, updated_at: 200, preview: "sk-…abcd" },
        usage: metrics(1_200, 1_500)
      }]
    }
  };
}

function usageBreakdown() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    collection_started_at: 90,
    effective_start_at: 100,
    definition: "user_model_reasoning_effort_tokens",
    account: null,
    user: "alice@example.com",
    totals: metrics(1_200, 1_500),
    models: [],
    reasoning_efforts: [],
    combinations: [{ ...metrics(1_200, 1_500), account: "alpha", model: "gpt-5.6", reasoning_effort: "high" }]
  };
}

function teamUsageCatalog() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    attribution: "current_membership",
    teams: [{
      id: "team_platform",
      name: "Platform",
      description: "Core",
      tag_style: "rose",
      user_count: 1,
      current_user_count: 1,
      created_at: 100,
      updated_at: 200,
      usage: { ...metrics(1_200, 1_500), active_users: 1 }
    }]
  };
}

function teamUsageBreakdown() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    definition: "team_model_reasoning_effort_tokens",
    team_id: "team_platform",
    attribution: "current_membership",
    totals: metrics(1_200, 1_500),
    users: [{ user: "alice@example.com", ...metrics(1_200, 1_500) }],
    accounts: [{ account: "alpha", ...metrics(1_200, 1_500) }],
    models: [{ model: "gpt-5.6", ...metrics(1_200, 1_500) }],
    combinations: [{ model: "gpt-5.6", reasoning_effort: "high", ...metrics(1_200, 1_500) }],
    series: { start_at: 100, end_at: 300, bucket_seconds: 60, buckets: [100, 160, 220], values: [200, 600, 700] }
  };
}

function weeklyQuota(mode: "inherit" | "custom" = "inherit", policyTokens: number | null = null) {
  const effectiveLimit = mode === "custom" ? policyTokens : 1_000;
  return {
    period: "natural_week",
    timezone: "Asia/Shanghai",
    week_start_at: 100,
    week_end_at: 700,
    limit_tokens: effectiveLimit,
    base_limit_tokens: effectiveLimit,
    bonus_tokens: 200,
    used_tokens: 100,
    weighted_used_tokens: 100,
    raw_used_tokens: 80,
    unweighted_used_tokens: 80,
    weighted_raw_used_tokens: 100,
    usage_reset_tokens: 0,
    remaining_tokens: effectiveLimit === null ? null : effectiveLimit - 100,
    used_percent: effectiveLimit === null ? null : 10,
    limit_reached: false,
    source: mode === "custom" ? "user_custom" : "default",
    policy_mode: mode,
    policy_tokens: policyTokens,
    policy_updated_at: mode === "custom" ? 200 : null,
    policy_updated_by: mode === "custom" ? "admin" : null,
    policy_reset_at: mode === "custom" ? 700 : null,
    default_limit_tokens: 1_000,
    unlimited: false,
    soft_limit: true,
    quota_unit: "weighted_tokens",
    adjustment_count: 1,
    personal_policy_reset_enabled: true
  };
}

function quotaResult(mode: "inherit" | "custom" = "inherit", policyTokens: number | null = null) {
  return {
    user: "alice@example.com",
    weekly_quota: weeklyQuota(mode, policyTokens),
    adjustments: [{ action: "bonus", token_amount: 200, reason: "临时项目扩容", created_at: 150, created_by: "admin" }]
  };
}

function metrics(totalTokens: number, weightedTokens: number) {
  return {
    request_count: 4,
    success_count: 3,
    failed_count: 1,
    input_tokens: 500,
    output_tokens: 300,
    reasoning_tokens: 300,
    cached_tokens: 100,
    total_tokens: totalTokens,
    weighted_tokens: weightedTokens,
    last_used_at: 250
  };
}

function oneTimeKey(key: string, user: string) {
  return { label: `${user}:alpha`, account: "alpha", account_email: "alpha@example.com", user, status: "active", created_at: 100, updated_at: 200, preview: "sk-…abcd", key };
}

function requestPaths(fetchMock: ReturnType<typeof userFetchMock>) {
  return fetchMock.mock.calls.map(([input]) => String(input));
}

function request(fetchMock: ReturnType<typeof userFetchMock>, path: string, method: string) {
  return fetchMock.mock.calls.find(([input, init]) => String(input) === path && init?.method === method);
}

function jsonResponse(payload: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(payload), {
    ...init,
    status: init.status ?? 200,
    headers: { "Content-Type": "application/json", ...init.headers }
  });
}
