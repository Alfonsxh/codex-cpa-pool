import { screenshotExpect } from "./screenshot-expect";
import { installUsageVisualBackend } from "./usage-fixtures";
import { expect, test, type Page } from "@playwright/test";

// No saved preference, even though the browser itself is Chinese.
test.use({ storageState: { cookies: [], origins: [] }, locale: "zh-CN" });

async function login(page: Page, path: string) {
  await page.goto(path);
  await expect(page.getByRole("heading", { name: "Sign in to Admin" })).toBeVisible();
  await page.getByLabel("Management key", { exact: true }).fill("visual-preview");
  await page.getByRole("button", { name: "Verify & sign in" }).click();
  await expect(page.locator(".app-shell, .onboarding-app-shell")).toBeVisible();
}

async function mockUsageLogin(page: Page) {
  await page.route("**/usage/session", (route) => route.fulfill({ status: 401, json: { error: { code: "session_missing", message: "请先登录" } } }));
}

async function fitsViewport(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual((page.viewportSize()?.width ?? 1280) + 1);
  const bounds = await page.evaluate(() => ({ width: innerWidth, content: document.documentElement.scrollWidth }));
  expect(bounds.content).toBeLessThanOrEqual(bounds.width + 1);
  const picker = page.getByRole("combobox", { name: "Language", exact: true }).first();
  await expect(picker).toBeVisible();
  const box = await picker.boundingBox();
  expect(box?.width).toBeGreaterThan(60);
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(bounds.width + 1);
  const clippedNavigation = await page.locator(".admin-nav-item").evaluateAll((links) => links.filter((link) => (link.lastElementChild?.getBoundingClientRect().width ?? 0) > link.clientWidth).map((link) => link.textContent));
  expect(clippedNavigation, "Navigation labels must fit their links").toEqual([]);
}

test("English default, language switching and shared preference across all entries", async ({ page }) => {
  await mockUsageLogin(page);
  await page.goto("http://127.0.0.1:5192/");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByRole("heading", { name: "Choose your workspace" })).toBeVisible();
  await page.getByRole("combobox", { name: "Language", exact: true }).selectOption("zh-CN");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("heading", { name: "选择要进入的界面" })).toBeVisible();
  await page.getByRole("link", { name: /进入管理平台/ }).click();
  await expect(page).toHaveURL(/:5193\/admin/);
  await expect(page.getByRole("heading", { name: "进入管理中心" })).toBeVisible();
  await page.getByRole("combobox", { name: "语言", exact: true }).selectOption("en");
  await expect(page.getByRole("heading", { name: "Sign in to Admin" })).toBeVisible();
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await page.getByRole("link", { name: "Open Usage Center →" }).click();
  await expect(page).toHaveURL(/:5194\/usage/);
  await expect(page.getByRole("heading", { name: "Sign in to Usage Center" })).toBeVisible();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "Language", exact: true }).selectOption("zh-CN");
  await expect(page.getByRole("heading", { name: "登录使用中心" })).toBeVisible();
  await page.goto("http://127.0.0.1:5192/native/");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await page.getByRole("combobox", { name: "语言", exact: true }).selectOption("en");
  await expect(page.getByRole("heading", { name: "CPA accounts", exact: true })).toBeVisible();
});

test("switching keeps the authenticated route, filters and theme", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("cpa-ui-theme", "dark"));
  await login(page, "/admin/configuration?section=quota#limits");
  await expect(page.getByRole("checkbox", { name: "Preserve personal quota changes" })).toBeVisible();
  await page.getByRole("combobox", { name: "Language", exact: true }).selectOption("zh-CN");
  await expect(page.getByRole("checkbox", { name: "保留修改后的额度" })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("section")).toBe("quota");
  expect(new URL(page.url()).hash).toBe("#limits");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.getByRole("combobox", { name: "语言", exact: true }).selectOption("en");
  await expect(page.getByRole("checkbox", { name: "Preserve personal quota changes" })).toBeVisible();
  // The business option's persisted ID/value is independent of display language.
  const row = page.locator('[data-configuration-field="user_quota.preserve_personal_weekly_on_new_week"]');
  await expect(row).toContainText("Applies to all users.");
  await expect(row).toContainText("Monday at 00:00");
});

for (const viewport of [{ name: "desktop", width: 1440, height: 900 }, { name: "narrow", width: 1024, height: 768 }, { name: "mobile", width: 390, height: 844 }]) {
  for (const theme of ["light", "dark"] as const) {
    test(`English interface matrix ${viewport.name} ${theme}`, async ({ page }, info) => {
      test.setTimeout(90_000);
      await mockUsageLogin(page);
      await page.setViewportSize(viewport);
      await page.addInitScript((value) => localStorage.setItem("cpa-ui-theme", value), theme);
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await login(page, "/admin/overview");
      const residue: Record<string, string[]> = {};
      for (const route of ["overview", "accounts", "users", "teams", "runtime", "configuration", "setup"]) {
        await page.goto(`/admin/${route}`);
        await expect(page.locator(".app-shell, .onboarding-app-shell")).toBeVisible();
        await expect(page.locator(".page-content")).toBeVisible();
        if (route !== "setup") await expect(page.locator(".top-bar-refresh-state")).not.toHaveText("Refreshing");
        await expect(page.locator("html")).toHaveAttribute("lang", "en");
        await fitsViewport(page);
        await expect(page.locator(".skeleton-table")).toHaveCount(0);
        residue[route] = (await page.locator("body").innerText()).split("\n").filter((line) => /\p{Script=Han}/u.test(line));
        if (viewport.name !== "narrow") await screenshotExpect(page).toHaveScreenshot(`english-${route}-${viewport.name}-${theme}.png`, { fullPage: false });
      }
      await page.goto("http://127.0.0.1:5192/");
      await expect(page.getByRole("heading", { name: "Choose your workspace" })).toBeVisible();
      await fitsViewport(page);
      if (viewport.name !== "narrow") await screenshotExpect(page).toHaveScreenshot(`english-portal-${viewport.name}-${theme}.png`);
      await page.goto("http://127.0.0.1:5194/usage/");
      await expect(page.getByRole("heading", { name: "Sign in to Usage Center" })).toBeVisible();
      await fitsViewport(page);
      if (viewport.name !== "narrow") await screenshotExpect(page).toHaveScreenshot(`english-usage-login-${viewport.name}-${theme}.png`);
      await installUsageVisualBackend(page);
      await page.goto("http://127.0.0.1:5194/usage/");
      await expect(page.getByRole("tab", { name: "Account details", exact: true })).toBeVisible();
      await fitsViewport(page);
      const heading = await page.locator(".usage-brand-block").boundingBox();
      const actions = await page.locator(".usage-user-actions").boundingBox();
      expect(heading && actions && (heading.x + heading.width <= actions.x || heading.y + heading.height <= actions.y), "Usage heading must not overlap language and account controls").toBe(true);
      residue.usage = (await page.locator("body").innerText()).split("\n").filter((line) => /\p{Script=Han}/u.test(line));
      if (viewport.name !== "narrow") await screenshotExpect(page).toHaveScreenshot(`english-usage-${viewport.name}-${theme}.png`);
      await page.getByRole("tab", { name: "Daily usage", exact: true }).click();
      await page.getByRole("button", { name: "Model + reasoning effort", exact: true }).click();
      await expect(page.getByRole("img", { name: /Personal daily token trend/ })).toBeVisible();
      if (viewport.name !== "narrow") await screenshotExpect(page).toHaveScreenshot(`english-trend-${viewport.name}-${theme}.png`);
      await info.attach("untranslated-text-audit", { body: JSON.stringify(residue, null, 2), contentType: "application/json" });
      // These are the language's own name and user-authored synthetic team data.
      const preservedContent = new Set(["简体中文", "平台研发", "数据智能", "产品设计", "核心平台与基础设施", "数据产品与分析", "产品与体验设计"]);
      for (const [route, lines] of Object.entries(residue)) {
        expect(lines.filter((line) => !preservedContent.has(line)), `${route}: untranslated interface text`).toEqual([]);
      }
      expect(errors).toEqual([]);
    });
  }
}

test("localized multiplier units keep the same numeric values and emphasis", async ({ page }) => {
  await login(page, "/admin/configuration?section=model-multipliers");
  await expect(page.locator(".configuration-unit-label").first()).toHaveText("×");
  await expect(page.locator(".configuration-multiplier-elevated").first()).toBeVisible();
  const values = await page.locator('input[type="number"]').evaluateAll((inputs) => inputs.map((input) => (input as HTMLInputElement).value));
  const english = await page.locator(".page-content").innerText();
  expect(english).not.toMatch(/\p{Script=Han}/u);
  await page.getByRole("combobox", { name: "Language", exact: true }).selectOption("zh-CN");
  await expect(page.locator(".configuration-unit-label").first()).toHaveText("倍");
  await expect(page.locator(".configuration-multiplier-elevated").first()).toBeVisible();
  expect(await page.locator('input[type="number"]').evaluateAll((inputs) => inputs.map((input) => (input as HTMLInputElement).value))).toEqual(values);
});
