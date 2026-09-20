import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import type { ConfigurationCatalog } from "../api/configuration";
import { ConfigurationPage } from "./ConfigurationPage";

describe("ConfigurationPage", () => {
  it("refreshes scheduler status without overwriting an unsaved notification switch or Webhook", async () => {
    let worker = "heartbeat_lost";
    let unavailable = false;
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      if (path === "/admin/api/settings/notifications") {
        if (unavailable) throw new Error("status temporarily unavailable");
        const payload = await supportingSettingsResponse(path)!.json();
        payload.notifications.worker_status = worker;
        return jsonResponse(payload);
      }
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      return jsonResponse(configurationFixture());
    }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, "/configuration?key=notification.enabled", client);
    expect(await screen.findByText("心跳中断")).toBeInTheDocument();
    const enabled = screen.getByLabelText("启用企业微信通知");
    await user.click(enabled);
    const webhook = screen.getByLabelText("Webhook 地址");
    await user.type(webhook, "https://example.test/draft");
    worker = "running";
    await act(async () => { await client.refetchQueries({ queryKey: ["notification-settings"] }); });
    expect(await screen.findByText("待命中")).toBeInTheDocument();
    expect(screen.getByText("已关闭", { exact: true })).toBeInTheDocument();
    expect(enabled).toBeChecked();
    expect(webhook).toHaveValue("https://example.test/draft");
    expect(webhook).toHaveFocus();
    expect(screen.getByText("1 项未保存")).toBeInTheDocument();
    unavailable = true;
    await act(async () => { await client.refetchQueries({ queryKey: ["notification-settings"] }); });
    expect(await screen.findByText("状态未知")).toBeInTheDocument();
    expect(screen.getByText("暂时无法刷新调度状态，请稍后重试。")).toBeInTheDocument();
    expect(enabled).toBeChecked();
    expect(webhook).toHaveValue("https://example.test/draft");
  });
  it("opens the deep-linked access section used by first-run guidance", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      throw new Error(`unexpected request: ${path}`);
    }));
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, "/configuration?section=access");

    const access = within(await screen.findByRole("navigation", { name: "配置分类" }))
      .getByRole("button", { name: "系统设置" });
    await waitFor(() => expect(access).toHaveAttribute("aria-expanded", "true"));
    expect(screen.getByRole("button", { name: "设置用户初始密码" })).toBeInTheDocument();
  });

  it("loads only the complete fine-grained catalog, masks secrets, searches and saves changed live fields", async () => {
    let current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { confirm: string; values: Record<string, unknown> };
        current = withUpdatedValues(current, body.values);
        return jsonResponse({
          message: "已保存 1 项配置",
          changed: Object.keys(body.values),
          applied: ["live"],
          pending_deployment: false
        });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    expect(await screen.findByPlaceholderText("已配置；留空保持不变")).toHaveValue("");
    expect(fetchMock).toHaveBeenCalledTimes(4);

    await user.type(screen.getByLabelText("搜索配置"), "产品名称{Enter}");
    const productName = await screen.findByLabelText("产品名称");
    await user.clear(productName);
    await user.type(productName, "CPA Control");
    expect(screen.getByText("1 项未保存")).toBeInTheDocument();
    productName.focus();
    await user.keyboard("{Enter}");
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    await user.click(screen.getByRole("button", { name: "保存配置" }));

    expect(await screen.findByText("已保存 1 项配置")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(6));
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe("/admin/api/settings/configuration");
    expect(post?.[1]).toMatchObject({
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({
      confirm: "save",
      values: { "branding.product_name": "CPA Control" }
    });
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/settings")).toBe(false);
  });

  it("requires an impact confirmation before saving account rebuild fields", async () => {
    const current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        return jsonResponse({
          message: "已保存 1 项配置",
          changed: ["cpa.request_retry"],
          applied: ["accounts"],
          pending_deployment: false
        });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    const retry = await screen.findByLabelText("请求重试次数");
    await user.clear(retry);
    await user.type(retry, "3");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    expect(await screen.findByRole("dialog", { name: "保存 1 项配置？" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "保存并应用" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
  });

  it("keeps apply failures visible in the confirmation dialog and lets the operator close it", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        return new Response(JSON.stringify({
          error: { message: "配置应用失败，已尝试恢复原配置", type: "request_error", code: "configuration_apply_failed" }
        }), { status: 502, headers: { "Content-Type": "application/json" } });
      }
      return jsonResponse(configurationFixture());
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    const retry = await screen.findByLabelText("请求重试次数");
    await user.clear(retry);
    await user.type(retry, "3");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    await user.click(screen.getByRole("button", { name: "保存并应用" }));

    expect(await screen.findByText("配置应用失败，已尝试恢复原配置")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "保存 1 项配置？" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "保存 1 项配置？" })).not.toBeInTheDocument());
  });

  it("renders field descriptions and places the WeCom switch before the webhook editor", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    await screen.findByLabelText("请求重试次数");
    expect(screen.queryByText("统一作用于所有业务 CPA。")).not.toBeInTheDocument();
    expect(screen.getByText("上游失败重试次数。")).toBeInTheDocument();
    expect(screen.queryByText("branding.product_name", { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText("默认 Codex CPA Pool", { exact: true })).not.toBeInTheDocument();
    await selectConfigurationItem(user, "通知设置", "企业微信通知");
    const enabled = screen.getByLabelText("启用企业微信通知");
    const webhook = screen.getByText("企业微信群 Webhook");
    expect(enabled.compareDocumentPosition(webhook) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("clears the Webhook independently without restoring its enabled switch or losing other drafts", async () => {
    let configured = true;
    let current = withUpdatedValues(configurationFixture(), { "notification.enabled": true });
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/notification-webhook/clear") {
        configured = false;
        current = withUpdatedValues(current, { "notification.enabled": false });
        return jsonResponse({ message: "企业微信 Webhook 已清除，通知已关闭" });
      }
      if (path === "/admin/api/settings/notifications") {
        const payload = await supportingSettingsResponse(path)!.json();
        payload.notifications.webhook_configured = configured;
        return jsonResponse(payload);
      }
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        current = withUpdatedValues(current, body.values);
        return jsonResponse({ message: "已保存品牌", changed: Object.keys(body.values), applied: ["live"], pending_deployment: false });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, "/configuration");
    await user.type(await screen.findByLabelText("产品名称"), " Updated");
    await selectConfigurationItem(user, "通知设置", "企业微信通知");
    expect(screen.getByLabelText("启用企业微信通知")).toBeChecked();
    await user.click(screen.getByRole("button", { name: "清除 Webhook" }));
    await user.click(screen.getByRole("button", { name: "确认清除" }));
    expect(await screen.findByText("企业微信 Webhook 已清除，通知已关闭")).toBeInTheDocument();
    expect(screen.getByLabelText("启用企业微信通知")).not.toBeChecked();
    expect(screen.getByText("1 项未保存")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    expect(await screen.findByText("已保存品牌")).toBeInTheDocument();
    const saved = fetchMock.mock.calls.find(([path, init]) => String(path) === "/admin/api/settings/configuration" && init?.method === "POST");
    expect(JSON.parse(String(saved?.[1]?.body)).values).toEqual({ "branding.product_name": "Codex CPA Pool Updated" });
  });

  it("loads the destructive all-user impact only when User Quota is opened and refreshes it after reset", async () => {
    let quotaReads = 0;
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      if (path === "/admin/api/users/quota-actions" && init?.method === "POST") {
        return jsonResponse({
          action: "reset_usage",
          applied_users: ["alice@example.com", "bob@example.com"],
          skipped_users: [],
          message: "已清零 2 位用户的本周已用量；将在下次采集后生效",
          quota_operations: quotaSummary(0)
        });
      }
      if (path === "/admin/api/users/quota-actions") {
        quotaReads += 1;
        return jsonResponse(quotaSummary(quotaReads === 1 ? 2 : 0));
      }
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    await screen.findByPlaceholderText("已配置；留空保持不变");
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/users/quota-actions")).toBe(false);
    expect(screen.getByRole("button", { name: "用量与额度" })).toHaveAttribute("aria-expanded", "true");
    expect(quotaReads).toBe(0);
    await user.click(screen.getByRole("button", { name: "额度" }));
    const impact = await screen.findByLabelText("本周用量清零影响范围");
    expect(within(impact).getByText("有用量用户").nextElementSibling).toHaveTextContent("2位");
    await user.click(screen.getByRole("button", { name: "清零全部用户本周已用量" }));
    const reason = screen.getByLabelText("操作原因");
    await user.type(reason, "incident{Enter}correction");
    expect(reason).toHaveValue("incident\ncorrection");
    await user.type(screen.getByLabelText("清零确认文字"), "RESET ALL USERS");
    await user.keyboard("{Enter}");
    expect(fetchMock.mock.calls.filter(([path, init]) => String(path) === "/admin/api/users/quota-actions" && init?.method === "POST")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "确认清零" }));

    expect(await screen.findByText("已清零 2 位用户的本周已用量；将在下次采集后生效")).toBeInTheDocument();
    await waitFor(() => expect(quotaReads).toBe(2));
    expect(screen.getByRole("button", { name: "当前无需清零" })).toBeDisabled();
  });

  it("keeps access, storage and audit in the same workspace and expires the session after key rotation", async () => {
    const rotated = vi.fn();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      if (path === "/admin/api/settings/management-key" && init?.method === "POST") {
        return jsonResponse({ message: "管理密钥已更新，请重新登录", rotated: true, services: 0 });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" onManagementKeyRotated={rotated} />);

    expect(await screen.findByRole("button", { name: "数据与审计" })).toHaveAttribute("aria-expanded", "true");
    const storageButton = screen.getByRole("button", { name: /本地数据/ });
    await user.click(storageButton);
    expect(storageButton).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("region", { name: "持久化数据" })).toBeInTheDocument();
    expect(screen.getByText("state/control-plane.sqlite3")).toBeInTheDocument();
    const auditButton = screen.getByRole("button", { name: /审计记录/ });
    await user.click(auditButton);
    expect(auditButton).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("region", { name: "最近管理操作" })).toBeInTheDocument();
    expect(screen.getByText("configuration.update")).toBeInTheDocument();
    await selectConfigurationItem(user, "系统设置", "访问凭据");
    await user.click(screen.getByRole("button", { name: "更换管理密钥" }));
    const newKey = screen.getByLabelText("新管理密钥");
    const confirmation = screen.getByLabelText("再次输入管理密钥");
    await user.type(newKey, "replacement-key-2026");
    await user.tab();
    expect(confirmation).toHaveFocus();
    await user.type(confirmation, "replacement-key-2026");
    await user.tab();
    expect(screen.getByRole("button", { name: "更新并重新进入" })).toHaveFocus();
    await user.keyboard("{Enter}");

    await waitFor(() => expect(rotated).toHaveBeenCalledWith("管理密钥已更新，请重新登录"));
    const request = fetchMock.mock.calls.find(([path]) => String(path) === "/admin/api/settings/management-key");
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({ new_key: "replacement-key-2026", confirmation: "replacement-key-2026" });
  });

  it("presents an informative and recoverable audit empty state", async () => {
    let workspaceReads = 0;
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      if (path === "/admin/api/settings/workspace") {
        workspaceReads += 1;
        return jsonResponse({
          storage: [{ label: "控制面数据库", path: "state/control-plane.sqlite3", exists: true, mode: "600" }],
          backups: { count: 1, latest: "backups/accounts/fixture" },
          recent_audit: []
        });
      }
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    expect(await screen.findByRole("button", { name: "数据与审计" })).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("button", { name: /审计记录/ }));
    expect(screen.getByRole("region", { name: "最近管理操作" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "暂无管理操作" })).toBeInTheDocument();
    expect(screen.getByText("配置与维护操作将在此记录。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "刷新审计记录" }));

    await waitFor(() => expect(workspaceReads).toBe(2));
  });

  it("keeps modified quotas only when the global retention control is enabled", async () => {
    let current = configurationFixture();
    const saved: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { values: Record<string, unknown> };
        saved.push(body.values);
        current = withUpdatedValues(current, body.values);
        return jsonResponse({ message: "已保存 1 项配置", changed: Object.keys(body.values), applied: ["quota"], pending_deployment: false });
      }
      return jsonResponse(current);
    }));
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, "/configuration?section=quota");
    const retention = await screen.findByRole("checkbox", { name: /保留修改后的额度/ });
    expect(retention).not.toBeChecked();
    expect(screen.queryByLabelText("新周恢复默认个人额度")).not.toBeInTheDocument();
    for (const preserve of [true, false]) {
      await user.click(retention);
      await user.click(screen.getByRole("button", { name: "保存配置" }));
      await user.click(await screen.findByRole("button", { name: "保存并应用" }));
      await waitFor(() => expect(screen.getByText("未修改")).toBeInTheDocument());
      expect(retention).toHaveProperty("checked", preserve);
      expect(saved.at(-1)).toEqual({ "user_quota.preserve_personal_weekly_on_new_week": preserve });
    }
  });

  it("shows model multipliers and saves Astra changes as quota policy", async () => {
    let current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { values: Record<string, unknown> };
        current = withUpdatedValues(current, body.values);
        return jsonResponse({
          message: "已保存 1 项配置",
          changed: Object.keys(body.values),
          applied: ["quota"],
          pending_deployment: false
        });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    expect(await screen.findByRole("button", { name: "用量与额度" })).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("button", { name: "模型倍率" }));
    expect(screen.getByLabelText("gpt-6-astra用户额度倍率")).toHaveValue(4);
    expect(screen.getByLabelText("gpt-5.6-sol用户额度倍率")).toHaveValue(1);
    expect(screen.getByLabelText("其他未匹配模型用户额度倍率")).toHaveValue(1);

    const astra = screen.getByLabelText("gpt-6-astra用户额度倍率");
    await user.clear(astra);
    await user.type(astra, "5");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    expect(await screen.findByText(/用户额度下次采集后生效/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存并应用" }));

    await waitFor(() => expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({
      confirm: "save",
      values: { "user_quota.model_multiplier.gpt-6-astra": 5 }
    });
  });

  it("preserves cross-category drafts, opens hidden search results and discards everything from data and audit", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      return supportingSettingsResponse(path) ?? jsonResponse(configurationFixture());
    }));
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, "/configuration");
    const navigation = within(await screen.findByRole("navigation", { name: "配置分类" }));
    expect(navigation.getAllByRole("button").filter((button) => button.hasAttribute("aria-expanded")).map((button) => button.textContent)).toEqual([
      "品牌与身份", "系统设置", "CPA 容器", "请求与账号", "用量与额度", "通知设置", "数据与审计"
    ]);
    const product = await screen.findByLabelText("产品名称");
    expect(navigation.getByRole("button", { name: "站点品牌" })).toHaveAttribute("aria-current", "page");
    await user.click(navigation.getByRole("button", { name: "系统设置" }));
    expect(product).toBeVisible();
    expect(navigation.getByRole("button", { name: "系统设置" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("heading", { name: "站点品牌" })).not.toBeInTheDocument();
    await user.clear(product);
    await user.type(product, "New Brand");
    await user.type(screen.getByLabelText("搜索配置"), "Max 推理强度颜色{Enter}");
    expect(navigation.getByRole("button", { name: "系统设置" })).toHaveAttribute("data-current", "true");
    const color = screen.getByRole("textbox", { name: "Max 推理强度颜色" });
    expect(color).toBeVisible();
    await user.clear(color);
    await user.type(color, "#123456");
    expect(screen.getByText("2 项未保存")).toBeInTheDocument();
    expect(screen.getByText("涉及 2 个分类")).toBeInTheDocument();
    await selectConfigurationItem(user, "品牌与身份", "站点品牌");
    expect(screen.getByLabelText("产品名称")).toHaveValue("New Brand");
    await selectConfigurationItem(user, "数据与审计", "审计记录");
    await user.click(screen.getByRole("button", { name: "撤销未保存修改" }));
    expect(screen.getByRole("button", { name: "保存配置" })).toBeDisabled();
    await selectConfigurationItem(user, "品牌与身份", "站点品牌");
    expect(screen.getByLabelText("产品名称")).toHaveValue("Codex CPA Pool");
  });

  it("saves only edited fields across categories and rejects an empty required number even when zero is valid", async () => {
    let current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        current = withUpdatedValues(current, body.values);
        return jsonResponse({ message: "已保存 2 项配置", changed: Object.keys(body.values), applied: ["live", "accounts"], pending_deployment: false });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);
    await user.clear(await screen.findByLabelText("请求重试次数"));
    await selectConfigurationItem(user, "品牌与身份", "站点品牌");
    const product = screen.getByLabelText("产品名称");
    await user.clear(product);
    await user.type(product, "Changed Brand");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    expect(screen.getByLabelText("请求重试次数")).toHaveFocus();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
    await user.type(screen.getByLabelText("请求重试次数"), "0");
    await selectConfigurationItem(user, "数据与审计", "审计记录");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    await user.click(screen.getByRole("button", { name: "保存并应用" }));
    expect(await screen.findByText("已保存 2 项配置")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ confirm: "save", values: { "branding.product_name": "Changed Brand", "cpa.request_retry": 0 } });
    await waitFor(() => expect(screen.getByRole("button", { name: "保存配置" })).toBeDisabled());
  });

  it.each([
    ["推理强度策略", "admin.account_usage.reasoning_effort_color.max", "系统设置"],
    ["推理强度策略", "user_quota.reasoning_multiplier.max", "用量与额度"]
  ])("resolves legacy %s links using the field's new category", async (group, key, category) => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => supportingSettingsResponse(String(input)) ?? jsonResponse(configurationFixture())));
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, `/configuration?group=${group}&key=${key}`);
    const navigation = within(await screen.findByRole("navigation", { name: "配置分类" }));
    await waitFor(() => expect(navigation.getByRole("button", { name: category })).toHaveAttribute("data-current", "true"));
    const target = document.querySelector(`[data-configuration-field="${key}"]`)!;
    expect(target).toBeVisible();
    expect(target.contains(document.activeElement)).toBe(true);
  });

  it("shows a recoverable empty state when the configuration catalog has no groups", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") {
        return jsonResponse({ version: 1, generated_at: 1_800_000_000, field_count: 0, groups: [] });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    await user.click(await screen.findByRole("button", { name: "品牌与身份" }));
    expect(await screen.findByText("当前没有可配置项")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "进入访问凭据" }));
    expect(screen.getByText("管理密钥已配置")).toBeInTheDocument();
  });
});

function configurationFixture(): ConfigurationCatalog {
  return {
    version: 1,
    generated_at: 1_800_000_000,
    field_count: 11,
    groups: [
      {
        name: "CPA 请求",
        description: "统一作用于所有业务 CPA。",
        fields: [
          {
            key: "cpa.proxy_url",
            label: "默认上游代理 URL",
            description: "加密保存，不会回显。",
            type: "proxy_url_secret",
            value: "",
            default: "",
            apply_mode: "accounts",
            editable: true,
            configured: true
          },
          {
            key: "cpa.request_retry",
            label: "请求重试次数",
            description: "上游失败重试次数。",
            type: "integer",
            value: 2,
            default: 2,
            apply_mode: "accounts",
            editable: true,
            min: 0,
            max: 10
          }
        ]
      },
      {
        name: "品牌与身份",
        description: "管理公开名称和客户端导出参数。",
        fields: [
          {
            key: "branding.product_name",
            label: "产品名称",
            description: "所有页面显示的完整名称。",
            type: "text",
            value: "Codex CPA Pool",
            default: "Codex CPA Pool",
            apply_mode: "live",
            editable: true,
            min_length: 2,
            max_length: 64
          }
        ]
      },
      {
        name: "账号自动切换",
        description: "额度耗尽后自动迁移路由。",
        fields: [
          {
            key: "account_failover.mode",
            label: "自动切换模式",
            description: "只保留关闭或自动执行。",
            type: "choice",
            value: "active",
            default: "active",
            apply_mode: "live",
            editable: true,
            choices: [
              { value: "off", label: "关闭" },
              { value: "active", label: "自动执行" }
            ]
          }
        ]
      },
      {
        name: "用户额度",
        description: "全部用户的系统默认周额度与网关故障策略。",
        fields: [
          {
            key: "user_quota.default_weekly_tokens",
            label: "用户周额度系统默认值",
            description: "个人未配置策略时使用。",
            type: "nullable_integer",
            value: 20_000_000,
            default: null,
            apply_mode: "quota",
            editable: true,
            unit: "Token",
            min: 1,
            max: 1_000_000_000_000
          },
          {
            key: "user_quota.preserve_personal_weekly_on_new_week",
            label: "保留修改后的额度",
            description: "对所有用户生效，关闭后下周恢复组织默认额度。",
            type: "boolean",
            value: false,
            default: false,
            apply_mode: "quota",
            editable: true
          }
        ]
      },
      {
        name: "推理强度策略",
        description: "模型与推理强度共同决定用户额度 Token 倍率。",
        fields: [
          multiplierField("user_quota.model_multiplier.gpt-6-astra", "gpt-6-astra 模型倍率", 4),
          multiplierField("user_quota.model_multiplier.gpt-5.6-sol", "gpt-5.6-sol 模型倍率", 1),
          multiplierField("user_quota.model_multiplier.unknown", "其他 / 未匹配模型 模型倍率", 1),
          multiplierField("user_quota.reasoning_multiplier.max", "Max 推理强度倍率", 2),
          {
            key: "admin.account_usage.reasoning_effort_color.max",
            label: "Max 推理强度颜色",
            description: "账号明细显示颜色。",
            type: "color",
            value: "#b2731e",
            default: "#b2731e",
            apply_mode: "live",
            editable: true
          }
        ]
      },
      {
        name: "企业微信通知",
        description: "企业微信通知配置。",
        fields: [
          {
            key: "notification.enabled",
            label: "启用企业微信通知",
            description: "启用定时通知。",
            type: "boolean",
            value: false,
            default: false,
            apply_mode: "live",
            editable: true
          }
        ]
      }
    ]
  };
}

function multiplierField(key: string, label: string, value: number): ConfigurationCatalog["groups"][number]["fields"][number] {
  return {
    key,
    label,
    description: "新采集事件的 Token 倍率。",
    type: "number",
    value,
    default: value,
    apply_mode: "quota",
    editable: true,
    unit: "倍",
    min: 0.1,
    max: 10
  };
}

function withUpdatedValues(catalog: ConfigurationCatalog, values: Record<string, unknown>): ConfigurationCatalog {
  return {
    ...catalog,
    generated_at: catalog.generated_at + 1,
    groups: catalog.groups.map((group) => ({
      ...group,
      fields: group.fields.map((field) => Object.prototype.hasOwnProperty.call(values, field.key)
        ? { ...field, value: values[field.key] as never }
        : field)
    }))
  };
}

async function selectConfigurationItem(user: ReturnType<typeof userEvent.setup>, category: string, section: string) {
  const navigation = within(screen.getByRole("navigation", { name: "配置分类" }));
  const categoryButton = navigation.getByRole("button", { name: category });
  if (categoryButton.getAttribute("aria-expanded") !== "true") await user.click(categoryButton);
  await user.click(navigation.getByRole("button", { name: section }));
}

function renderConfiguration(element: React.ReactNode, entry = "/configuration?group=CPA 请求", client?: QueryClient) {
  const queryClient = client ?? new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
    </MemoryRouter>
  );
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function supportingSettingsResponse(path: string) {
  if (path === "/admin/api/users/quota-actions") return jsonResponse(quotaSummary(0));
  if (path === "/admin/api/settings/general") {
    return jsonResponse({
      version: 1,
      apply_mode: "live",
      generated_at: 1_800_000_000,
      values: {
        product_name: "Codex CPA Pool",
        short_name: "CCPA",
        environment_label: "Test",
        public_base_url: "https://example.test",
        allowed_email_domains: ["example.com"],
        key_prefix: "ccpa_",
        provider_name: "Codex CPA Pool",
        api_key_env: "CCPA_API_KEY",
        default_model: "gpt-5.6-sol"
      },
      security: { management_key_configured: true, initial_password_configured: true },
      branding: { custom_logo: false }
    });
  }
  if (path === "/admin/api/settings/notifications") {
    return jsonResponse({
      notifications: { webhook_configured: false, webhook_url: "", heartbeat_at: null, last_success_at: null, last_error: "", next_schedule_at: null },
      values: { enabled: false, timezone: "UTC", daily_times: "09:00", schedule_grace_minutes: 15, quota_alert_enabled: true, weekly_threshold_percent: 90 }
    });
  }
  if (path === "/admin/api/settings/workspace") {
    return jsonResponse({
      storage: [{ label: "控制面数据库", path: "state/control-plane.sqlite3", exists: true, mode: "600" }],
      backups: { count: 1, latest: "backups/accounts/fixture" },
      recent_audit: [{ timestamp: 1_800_000_000, action: "configuration.update", target: "settings", outcome: "accepted" }]
    });
  }
  return null;
}

function quotaSummary(usersWithUsage: number) {
  return {
    total_users: 3,
    users_with_usage: usersWithUsage,
    total_used_tokens: usersWithUsage ? 3_000_000 : 0,
    total_raw_used_tokens: usersWithUsage ? 2_000_000 : 0,
    users_with_personal_policy: 0,
    users_with_bonus: 0,
    users_with_usage_reset: 0,
    week_start_at: 1_799_900_000,
    week_end_at: 1_800_500_000
  };
}
