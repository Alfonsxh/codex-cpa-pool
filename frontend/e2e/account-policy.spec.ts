import { expect, test } from "@playwright/test";

for (const width of [1440, 390]) {
  for (const theme of ["light", "dark"]) {
    test(`停用账号显示处理状态、错误与重试 ${width} ${theme}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 900 });
      await page.addInitScript(value => localStorage.setItem("cpa-ui-theme", value), theme);
      let requests = 0;
      let submitted: Record<string, unknown> | undefined;
      let completeFirst: (() => void) | undefined;
      await page.route("**/admin/api/accounts/policy", async route => {
        submitted = route.request().postDataJSON();
        expect(route.request().headers()["x-csrf-token"]).toBeTruthy();
        requests++;
        if (requests === 1) {
          await new Promise<void>(resolve => { completeFirst = resolve; });
          await route.fulfill({ status: 409, json: { error: {
            code: "account_fallback_required", message: "请选择其他已启用 CPA 作为安全迁移目标"
          } } });
        } else {
          await route.fulfill({ json: { message: "CPA 账号选择策略已更新", account: {} } });
        }
      });
      await page.goto("/admin/accounts");
      await page.locator('input[type="password"]').fill("visual-preview");
      await page.getByRole("button", { name: "验证并进入" }).click();
      await page.getByRole("row", { name: /^展开 / }).first().click();
      await page.getByRole("button", { name: "停用账号", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: /^停用 / });
      await dialog.getByRole("button", { name: /^现有用户切换到：/ }).click();
      await expect(page.getByRole("listbox", { name: "现有用户切换到" })).toBeVisible();
      await page.getByRole("option").last().click();
      await dialog.getByRole("button", { name: "确认停用" }).click();
      await expect.poll(() => requests).toBe(1);
      await expect(dialog.getByRole("button", { name: "正在停用…" })).toBeDisabled();
      await expect(dialog.getByRole("button", { name: "取消", exact: true })).toBeDisabled();
      await expect(dialog.getByRole("status")).toHaveText("正在更新账号选择策略，请稍候。");
      expect(submitted).toMatchObject({ group_enabled: false, default_group: false });
      expect(submitted?.fallback_account).toBeTruthy();
      await page.keyboard.press("Escape");
      await expect(dialog).toBeVisible();
      await page.screenshot({ path: testInfo.outputPath("policy-pending.png") });
      completeFirst!();
      await expect(dialog.getByText("请选择其他已启用 CPA 作为安全迁移目标")).toBeVisible();
      await expect(dialog.getByRole("button", { name: "确认停用" })).toBeEnabled();
      await dialog.getByRole("button", { name: "确认停用" }).click();
      await expect.poll(() => requests).toBe(2);
      await expect(dialog).toBeHidden();
      await expect(page.getByText("CPA 账号选择策略已更新", { exact: true })).toBeVisible();
    });
  }
}
