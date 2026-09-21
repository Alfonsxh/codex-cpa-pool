import { expect, test } from "@playwright/test";

for (const width of [1440, 390]) for (const theme of ["light", "dark"]) {
  test(`账号 Ticket 抽屉、安装、缓存与停止保护 ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 });
    await page.addInitScript(value => localStorage.setItem("cpa-ui-theme", value), theme);
    const submitted: object[] = [];
    let stopped = false;
    let failStatus = false;
    const detailReads: string[] = [];
    await page.route("**/admin/api/extensions*", async route => {
      if (failStatus) { await route.fulfill({ status: 503, json: { error: { message: "unavailable" } } }); return; }
      const requested = new URL(route.request().url()).searchParams.get("account");
      if (requested) detailReads.push(requested);
      await route.fulfill({ json: { desired_version: "v0.2.0", bundled_version: "", checks: {}, accounts: ["cpa-main", "cpa-lab", "cpa-edge"].map(account => ({ account, enabled: true, running: !stopped, selected: false,
        installation: { version: account === "cpa-edge" ? "v0.1.0" : "", sha256: "", installed_at: 0, staging: false },
        runtime: requested === account ? { state: stopped ? "stopped" : "ready", checked_at: 1789860000, registered: true, version: "0.1.0", harvest_active: true, inject_active: true, harvest_reason: "ready", inject_reason: "ready", cached_count: 0, inflight_count: 0,
          entries: [{ model: "gpt-6-astra", last_http: 200, last_length: 312, reason: "length_mismatch", backoff_seconds: 600, injected_count: 0 }] } : undefined
      })) } });
    });
    const failedJob = { id: "account-ticket-job", name: "Ticket", action: "plugin-update", target: "cpa-edge", status: "failed", created_at: 1789860000, error: "插件加载失败，已恢复原配置。" };
    await page.route("**/admin/api/runtime/jobs", async route => {
      const body = route.request().postDataJSON(); submitted.push(body);
      await route.fulfill({ json: { message: "Submitted", reused: false, job: { ...failedJob, target: body.target } } });
    });
    await page.route("**/admin/api/runtime/jobs/account-ticket-job", route => route.fulfill({ json: { job: failedJob } }));
    await page.goto("/admin/accounts");
    await page.locator('input[type="password"]').fill("visual-preview");
    await page.getByRole("button", { name: "验证并进入" }).click();
    const search = page.getByRole("textbox", { name: "搜索 CPA 账号" });
    await search.fill("cpa-edge");
    const first = page.getByRole("row", { name: "展开 cpa-edge", exact: true });
    await first.click();
    const link = page.getByRole("button", { name: "Codex Ticket", exact: true });
    await expect(link).toBeVisible();
    await expect(page.locator(".account-runtime-facts")).toContainText("已安装 · v0.1.0");
    await expect(page.getByRole("region", { name: "Ticket 插件详情" })).toHaveCount(0);
    expect(detailReads).toHaveLength(0);
    await page.screenshot({ path: testInfo.outputPath(`account-ticket-entry-${width}-${theme}.png`), animations: "disabled" });
    await link.focus();
    await link.press("Enter");
    const drawer = page.getByRole("dialog", { name: /Ticket 插件详情/ });
    await expect(drawer).toBeVisible();
    await expect(drawer).toContainText("cpa-edge");
    const panel = page.getByRole("region", { name: "Ticket 插件详情" });
    await expect(panel.getByText("长度不符合条件，未缓存")).toBeVisible();
    await expect(panel.getByText("暂无可用票据。插件已安装不代表已注入，需结合请求测试判断效果。")).toBeVisible();
    await expect(panel.getByRole("cell", { name: "已注入 0 次", exact: true })).toBeVisible();
    await expect(panel.getByRole("table", { name: "Ticket 插件状态", exact: true })).toBeVisible();
    await expect(panel.getByRole("table", { name: "Ticket 模型明细", exact: true })).toBeVisible();
    expect(submitted).toHaveLength(0);
    expect(new Set(detailReads)).toEqual(new Set(["cpa-edge"]));
    await panel.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath(`account-ticket-${width}-${theme}.png`), animations: "disabled" });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    await panel.getByRole("button", { name: "升级插件", exact: true }).click();
    await page.getByRole("dialog", { name: "升级插件", exact: true }).getByRole("button", { name: "取消", exact: true }).click();
    expect(submitted).toHaveLength(0);
    await panel.getByRole("button", { name: "升级插件", exact: true }).click();
    await page.getByRole("dialog", { name: "升级插件", exact: true }).getByRole("button", { name: "确定", exact: true }).click();
    await expect(panel.getByText("插件加载失败，已恢复原配置。")).toBeVisible();
    expect(submitted).toHaveLength(1);
    expect(submitted[0]).toMatchObject({ action: "plugin-update", target: "cpa-edge" });
    stopped = true;
    await panel.getByRole("button", { name: "刷新当前页", exact: true }).click();
    await expect(panel.getByRole("button", { name: "升级插件", exact: true })).toBeDisabled();
    await expect(panel.getByText("账号已停止，启动后可安装插件。此操作不会启动账号。")).toBeVisible();
    failStatus = true;
    await panel.getByRole("button", { name: "刷新当前页", exact: true }).click();
    await expect(panel.getByText("无法读取版本状态，请重试。")).toBeVisible();
    await expect(panel.getByRole("button", { name: "升级插件", exact: true })).toBeDisabled();
    expect(submitted).toHaveLength(1);
    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(search).toHaveValue("cpa-edge");
    await expect(page.getByRole("row", { name: "收起 cpa-edge", exact: true })).toHaveAttribute("aria-expanded", "true");
    await expect(link).toBeFocused();
    failStatus = false;
    stopped = false;
    await search.fill("cpa-main");
    await page.getByRole("row", { name: "展开 cpa-main", exact: true }).click();
    await page.getByRole("button", { name: "Codex Ticket", exact: true }).click();
    await expect(drawer).toContainText("cpa-main");
    await expect(panel.getByRole("button", { name: "安装插件", exact: true })).toBeEnabled();
    await expect(panel.getByText("插件加载失败，已恢复原配置。")).toHaveCount(0);
    expect(detailReads).toContain("cpa-main");
    expect(submitted).toHaveLength(1);
  });
}
