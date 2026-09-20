import { expect, test } from "@playwright/test";

for (const viewport of [{ name: "desktop", width: 1440, height: 1000 }, { name: "mobile", width: 390, height: 844 }]) {
  test(`插件版本检查与停止账号保护 ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const submitted: unknown[] = [];
    await page.route("**/admin/api/extensions", route => route.fulfill({ json: {
      checks: { cpa: { version: "v7.3.9", url: "https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.3.9", checked_at: 1789860000, attempted_at: 1789863600, error: "release_check_failed" }, "codex-ticket": { version: "v0.2.0", url: "https://github.com/Su-cyber-art/cpa-plugin-codex-ticket/releases/tag/v0.2.0", checked_at: 1789860000, attempted_at: 1789860000 } },
      desired_version: "v0.2.0", bundled_version: "v0.2.0-ccpa.1", accounts: [
        { account: "alpha", enabled: true, selected: true, running: true, installation: { version: "v0.1.0", sha256: "digest", installed_at: 1789800000, staging: false } },
        { account: "stopped", enabled: true, selected: true, running: false, installation: { version: "", sha256: "", installed_at: 0, staging: false } }
      ]
    } }));
    await page.route("**/admin/api/runtime/jobs", async route => {
      submitted.push(route.request().postDataJSON());
      await route.fulfill({ json: { message: "Task submitted", reused: false, job: { id: "plugin-job", action: "version-check", target: "all", status: "succeeded", created_at: 1789860000, output: "Version metadata checked; no software was installed." } } });
    });
    await page.route("**/admin/api/runtime/jobs/plugin-job", route => route.fulfill({ json: { job: { id: "plugin-job", action: "version-check", target: "all", status: "succeeded", created_at: 1789860000, output: "Version metadata checked; no software was installed." } } }));
    await page.goto("/admin/configuration?section=software");
    await page.locator('input[type="password"]').fill("visual-preview");
    await page.getByRole("button", { name: "验证并进入" }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("配置中心/请求与账号/插件与版本检查");
    await expect(page.getByText("最近检查失败；保留上次成功结果。")).toBeVisible();
    await expect(page.locator(".software-account-list li").filter({ hasText: "stopped" }).getByRole("button")).toBeDisabled();
    await page.getByRole("button", { name: "立即检查版本", exact: true }).click();
    await expect.poll(() => submitted.length).toBe(1);
    expect(submitted[0]).toEqual({ action: "version-check", target: "all", confirm: "version-check:all" });
    await expect(page.getByText("Version metadata checked; no software was installed.")).toBeVisible();
    await page.locator(".software-account-list li").filter({ hasText: "alpha" }).getByRole("button").click();
    await expect(page.getByRole("dialog")).toContainText("将为 alpha 安装 v0.2.0");
    expect(submitted).toHaveLength(1);
    await page.getByRole("button", { name: "取消", exact: true }).click();
    await expect(page.getByLabel("自动检查 CPA 版本", { exact: true })).toBeChecked();
    await expect(page.getByLabel("允许后台采票", { exact: true })).not.toBeChecked();
    expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
    await page.getByRole("button", {name:"立即检查版本",exact:true}).scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath(`software-${viewport.name}.png`), animations: "disabled" });
  });
}
