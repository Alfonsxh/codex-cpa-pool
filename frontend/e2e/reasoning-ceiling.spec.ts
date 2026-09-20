import { expect, test } from "@playwright/test";

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "mobile", width: 390, height: 844 }
]) {
  test(`最高推理强度保存与取消限制 ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    let catalog: { groups: Array<{ fields: Array<{ key: string; value: unknown }> }> } | undefined;
    const saves: Array<{ confirm: string; values: Record<string, unknown> }> = [];
    await page.route("**/admin/api/settings/configuration", async (route) => {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        saves.push(body);
        for (const group of catalog!.groups) for (const field of group.fields) {
          if (Object.hasOwn(body.values, field.key)) field.value = body.values[field.key];
        }
        await route.fulfill({ json: { message: "已保存 1 项配置", changed: Object.keys(body.values), applied: ["live"], pending_deployment: false } });
      } else {
        if (!catalog) catalog = await (await route.fetch()).json();
        await route.fulfill({ json: catalog });
      }
    });
    await page.goto("/admin/configuration?key=gateway.max_reasoning_effort");
    await page.locator('input[type="password"]').fill("visual-preview");
    await page.getByRole("button", { name: "验证并进入" }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("配置中心/请求与账号/请求策略");
    const row = page.locator('article[data-configuration-field="gateway.max_reasoning_effort"]');
    await expect(row).toContainText("max、ultra 映射为 xhigh");
    await expect(row).toContainText("较低等级保持原值");
    await expect(row).toContainText("不限制");
    for (const [value, label] of [["xhigh", "xhigh"], ["unlimited", "不限制"]]) {
      await row.getByRole("button", { name: /^最高推理强度：/ }).click();
      await page.getByRole("option", { name: `${label} · ${value}`, exact: true }).click();
      await page.getByRole("button", { name: "保存配置", exact: true }).click();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(page.getByText("已保存 1 项配置", { exact: true })).toBeVisible();
      await page.reload();
      await expect(row).toContainText(`${label} · ${value}`);
      await expect(page.getByRole("button", { name: "保存配置", exact: true })).toBeDisabled();
      if (value === "xhigh") {
        await row.scrollIntoViewIfNeeded();
        expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
        await page.screenshot({ path: testInfo.outputPath(`reasoning-ceiling-${viewport.name}.png`), animations: "disabled" });
      }
    }
    expect(saves).toEqual([
      { confirm: "save", values: { "gateway.max_reasoning_effort": "xhigh" } },
      { confirm: "save", values: { "gateway.max_reasoning_effort": "unlimited" } }
    ]);
  });
}
