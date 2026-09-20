import { expect, test, type Page } from "@playwright/test";
import type { ConfigurationCatalog } from "../src/api/configuration";

async function setupTicket(page: Page) {
  let catalog: ConfigurationCatalog;
  const saves: Array<{ confirm: string; values: Record<string, unknown> }> = [];
  const submitted: Array<{ action: string; target: string; confirm: string }> = [];
  const jobs = new Map<string, object>();
  const setting = (key: string) => catalog?.groups.flatMap(group => group.fields).find(field => field.key === key)?.value;
  await page.route("**/admin/api/settings/configuration", async route => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON(); saves.push(body);
      for (const group of catalog.groups) for (const field of group.fields) {
        if (Object.hasOwn(body.values, field.key)) field.value = body.values[field.key];
      }
      await route.fulfill({ json: { message: "配置已保存", changed: Object.keys(body.values), applied: ["live"], pending_deployment: false } });
    } else {
      if (!catalog) {
        catalog = await (await route.fetch()).json();
        catalog.groups.flatMap(group => group.fields).find(field => field.key === "plugins.codex_ticket.accounts")!.value = "qdata-new2,alpha,stopped,disabled";
      }
      await route.fulfill({ json: catalog });
    }
  });
  await page.route("**/admin/api/extensions", route => route.fulfill({ json: {
    checks: { cpa: { version: "v7.3.9", url: "https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.3.9", checked_at: 1789860000, attempted_at: 1789863600, error: "release_check_failed" }, "codex-ticket": { version: "v0.2.0", url: "https://github.com/Su-cyber-art/cpa-plugin-codex-ticket/releases/tag/v0.2.0", checked_at: 1789860000, attempted_at: 1789860000 } },
    desired_version: setting("plugins.codex_ticket.version") ?? "v0.2.0-ccpa.1", bundled_version: "v0.2.0-ccpa.1",
    accounts: [
      { account: "qdata-new2", enabled: true, running: true, version: "v0.2.0-ccpa.1" },
      { account: "alpha", enabled: true, running: true, version: "v0.1.0" },
      { account: "new-account", enabled: true, running: true, version: "" },
      { account: "stopped", enabled: true, running: false, version: "" },
      { account: "disabled", enabled: false, running: false, version: "" }
    ].map(({ version, ...row }) => ({ ...row, selected: String(setting("plugins.codex_ticket.accounts") ?? "").split(",").includes(row.account), installation: { version, sha256: "digest", installed_at: 1789800000, staging: false } }))
  } }));
  await page.route("**/admin/api/runtime/jobs", async route => {
    const body = route.request().postDataJSON(); submitted.push(body);
    const id = `ticket-job-${submitted.length}`;
    const job = { id, action: body.action, target: body.target, status: body.action === "plugin-update" ? "failed" : "succeeded", created_at: 1789860000,
      output: body.action === "plugin-update" ? "Activation failed; restoring the previous plugin configuration." : "Version metadata checked; no software was installed.",
      error: body.action === "plugin-update" ? "插件加载失败，已恢复原配置。" : undefined };
    jobs.set(id, job);
    await route.fulfill({ json: { message: "Task submitted", reused: false, job } });
  });
  await page.route("**/admin/api/runtime/jobs/ticket-job-*", route => route.fulfill({ json: { job: jobs.get(route.request().url().split("/").at(-1)!) } }));
  return { saves, submitted };
}
async function login(page: Page, section = "software") {
  await page.goto(`/admin/configuration?section=${section}`);
  await page.locator('input[type="password"]').fill("visual-preview");
  await page.getByRole("button", { name: "验证并进入" }).click();
}

for (const viewport of [{ name: "desktop", width: 1440, height: 1000 }, { name: "mobile", width: 390, height: 844 }]) {
  for (const theme of ["light", "dark"] as const) {
    test(`Ticket 单页保存安装与停止账号保护 ${viewport.name} ${theme}`, async ({ page }, testInfo) => {
      await page.setViewportSize(viewport);
      await page.addInitScript(value => localStorage.setItem("cpa-ui-theme", value), theme);
      const { saves, submitted } = await setupTicket(page);
      await login(page);
      await expect(page.getByRole("heading", { level: 1 })).toHaveText("配置中心/CPA 容器/Codex Ticket 插件");
      await expect(page.getByRole("tab")).toHaveCount(0);
      await expect(page.getByText("CLIProxyAPI", { exact: true })).toHaveCount(0);
      await expect(page.locator(".software-target-summary, .software-release-card")).toHaveCount(0);
      await expect(page.getByLabel("允许后台采票", { exact: true })).not.toBeChecked();
      await expect(page.locator(".ticket-advanced")).not.toHaveAttribute("open");
      await page.screenshot({ path: testInfo.outputPath(`ticket-settings-${viewport.name}-${theme}.png`), animations: "disabled" });
      const row = (account: string) => page.locator(`.ticket-account-row[data-account="${account}"]`);
      await expect(row("stopped").getByRole("button")).toBeDisabled();
      await expect(row("disabled").getByRole("button")).toBeDisabled();
      await expect(row("new-account").getByRole("button")).toBeDisabled();
      await expect(row("qdata-new2")).toContainText("已是目标版本");
      await row("new-account").getByRole("checkbox").check();
      await expect(row("new-account").getByRole("button")).toBeDisabled();
      await expect(row("alpha").getByRole("button")).toBeDisabled();
      await expect(page.getByText("请先保存配置，再安装或升级插件。")).toBeVisible();
      await page.getByRole("button", { name: "检查插件更新", exact: true }).click();
      await expect.poll(() => submitted.length).toBe(1);
      expect(submitted[0]).toEqual({ action: "version-check", target: "all", confirm: "version-check:all" });
      await expect(row("new-account").getByRole("checkbox")).toBeChecked();
      await page.getByRole("button", { name: "保存配置", exact: true }).click();
      await expect.poll(() => saves.length).toBe(1);
      expect(saves[0]).toEqual({ confirm: "save", values: { "plugins.codex_ticket.accounts": "qdata-new2,alpha,stopped,disabled,new-account" } });
      await expect(row("new-account").getByRole("button")).toBeEnabled();
      expect(submitted).toHaveLength(1);
      await row("new-account").getByRole("button").click();
      await expect(page.getByRole("dialog")).toContainText("将为 new-account 安装 v0.2.0-ccpa.1");
      await page.getByRole("button", { name: "取消", exact: true }).click();
      expect(submitted).toHaveLength(1);
      await page.getByRole("textbox", { name: "搜索账号", exact: true }).fill("alpha");
      await expect(page.locator(".ticket-account-row")).toHaveCount(1);
      await row("alpha").getByRole("button").click();
      await page.getByRole("dialog").getByRole("button", { name: "确定", exact: true }).click();
      await expect.poll(() => submitted.length).toBe(2);
      expect(submitted[1]).toEqual({ action: "plugin-update", target: "alpha", confirm: "plugin-update:alpha" });
      await expect(page.locator(".ticket-account-task")).toContainText("插件加载失败，已恢复原配置。");
      await page.getByRole("textbox", { name: "搜索账号", exact: true }).clear();
      await expect(row("stopped").getByRole("button")).toBeDisabled();
      await expect(row("disabled").getByRole("button")).toBeDisabled();
      await page.locator(".ticket-accounts").scrollIntoViewIfNeeded();
      expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
      expect(await page.locator(".settings-workspace-content").evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`ticket-accounts-${viewport.name}-${theme}.png`), animations: "disabled" });
      await page.reload();
      await expect(row("new-account").getByRole("checkbox")).toBeChecked();
      await page.goto("/admin/configuration?section=provisioning");
      await expect(page.getByText("CLIProxyAPI", { exact: true })).toBeVisible();
      await expect(page.getByText("最近检查失败；保留上次成功结果。")).toBeVisible();
      await expect(page.getByLabel("自动检查 CPA 版本", { exact: true })).toBeChecked();
      expect(submitted.filter(item => item.action === "plugin-update")).toHaveLength(1);
    });
  }
}

test("Ticket 高级字段旧链接与账号加载失败恢复", async ({ page }) => {
  await setupTicket(page);
  let fail = true;
  await page.route("**/admin/api/extensions", async route => {
    if (fail) await route.fulfill({ status: 503, json: { error: { message: "unavailable" } } });
    else await route.fallback();
  });
  await login(page, "software&key=plugins.codex_ticket.ttl_seconds");
  await expect(page.locator(".ticket-advanced")).toHaveAttribute("open");
  await expect(page.getByText("无法读取版本状态，请重试。")).toBeVisible();
  fail = false;
  await page.getByRole("button", { name: "刷新当前页", exact: true }).click();
  await expect(page.locator('.ticket-account-row[data-account="alpha"]')).toBeVisible();
});

test("Ticket 模型标签编辑与放弃修改不安装软件", async ({ page }) => {
  const { saves, submitted } = await setupTicket(page);
  await login(page, "codex-ticket");
  const models = page.getByRole("combobox", { name: "适用模型", exact: true });
  await models.fill("gpt-5.6-sol");
  await models.press("Enter");
  await expect(page.locator('[data-configuration-field="plugins.codex_ticket.models"]')).toContainText("gpt-5.6-sol");
  expect(saves).toHaveLength(0);
  await page.locator('.ticket-account-row[data-account="alpha"]').getByRole("checkbox").uncheck();
  await page.getByRole("button", { name: "撤销未保存修改", exact: true }).click();
  await expect(page.locator('[data-configuration-field="plugins.codex_ticket.models"]')).not.toContainText("gpt-5.6-sol");
  await expect(page.locator('.ticket-account-row[data-account="alpha"]').getByRole("checkbox")).toBeChecked();
  expect(saves).toHaveLength(0);
  expect(submitted).toHaveLength(0);
});
