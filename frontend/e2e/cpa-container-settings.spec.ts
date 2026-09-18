import { expect, test } from "@playwright/test";

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "mobile", width: 390, height: 844 }
]) {
  test(`CPA 容器六项配置集中展示并保存 ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    let catalog: { groups: Array<{ fields: Array<{ key: string; value: unknown }> }> } | undefined;
    const saves: Array<{ confirm: string; values: Record<string, unknown> }> = [];
    await page.route("**/admin/api/settings/configuration", async (route) => {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        saves.push(body);
        for (const group of catalog!.groups) {
          for (const field of group.fields) {
            if (Object.hasOwn(body.values, field.key)) field.value = body.values[field.key];
          }
        }
        await route.fulfill({ json: { message: "已保存 6 项配置", changed: Object.keys(body.values), applied: ["live", "accounts"], pending_deployment: false } });
      } else {
        if (!catalog) catalog = await (await route.fetch()).json();
        await route.fulfill({ json: catalog });
      }
    });
    await page.goto("/admin/configuration?key=cpa.passthrough_headers");
    await page.locator('input[type="password"]').fill("visual-preview");
    await page.getByRole("button", { name: "验证并进入" }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("配置中心/请求与账号/CPA 容器参数");
    const fields = [
      ["cpa.debug", "调试日志", "debug"],
      ["cpa.logging_to_file", "写入 CPA 日志文件", "logging-to-file"],
      ["cpa.usage_statistics_enabled", "官方用量事件", "usage-statistics-enabled"],
      ["cpa.passthrough_headers", "透传上游响应头", "passthrough-headers"],
      ["cpa.session_affinity", "会话亲和", "routing.session-affinity"],
      ["cpa.session_affinity_ttl", "会话亲和有效期", "routing.session-affinity-ttl"]
    ] as const;
    await expect(page.locator("article[data-configuration-field]")).toHaveCount(6);
    expect(catalog!.groups.flatMap((g) => g.fields).some((f) => f.key.startsWith("cpa.codex_default_"))).toBe(false);
    for (const [key, label, yamlKey] of fields) {
      const row = page.locator(`article[data-configuration-field="${key}"]`);
      await expect(row).toContainText(yamlKey);
      await row.scrollIntoViewIfNeeded();
      await expect(row.getByLabel(label, { exact: true })).toBeInViewport();
      if (key !== "cpa.session_affinity_ttl") await row.getByLabel(label, { exact: true }).click();
    }
    await page.getByLabel("会话亲和有效期", { exact: true }).fill("2");
    await page.getByRole("button", { name: "保存配置", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "保存 6 项配置？" })).toBeVisible();
    expect(saves).toHaveLength(0);
    await page.getByRole("button", { name: "保存并应用", exact: true }).click();
    await expect(page.getByText("已保存 6 项配置", { exact: true })).toBeVisible();
    expect(saves).toEqual([{ confirm: "save", values: {
      "cpa.debug": true,
      "cpa.logging_to_file": false,
      "cpa.usage_statistics_enabled": false,
      "cpa.passthrough_headers": false,
      "cpa.session_affinity": false,
      "cpa.session_affinity_ttl": "2h"
    } }]);
    await page.reload();
    await expect(page.getByLabel("调试日志", { exact: true })).toBeChecked();
    for (const label of ["写入 CPA 日志文件", "官方用量事件", "透传上游响应头", "会话亲和"]) await expect(page.getByLabel(label, { exact: true })).not.toBeChecked();
    await expect(page.getByLabel("会话亲和有效期", { exact: true })).toHaveValue("2");
    await expect(page.getByRole("button", { name: "保存配置", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
    await page.locator('article[data-configuration-field="cpa.passthrough_headers"]').scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath(`cpa-container-${viewport.name}.png`), animations: "disabled" });
    await page.goto("/admin/configuration?section=affinity");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("配置中心/请求与账号/CPA 容器参数");
  });
}
