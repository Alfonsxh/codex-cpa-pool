import { screenshotExpect } from "./screenshot-expect";
import { fulfillJSON, usageAccountsFixture, usageQuotaFixture, usageBreakdownFixture, usageTrendFixture, installUsageVisualBackend } from "./usage-fixtures";
import path from "node:path";
import { expect, test, type Page, type Route } from "@playwright/test";

const routes = [
  { slug: "overview", path: "/admin/overview", ready: "Token 使用", title: "运行总览" },
  { slug: "accounts", path: "/admin/accounts", ready: "更新通道", title: "账号管理" },
  { slug: "users", path: "/admin/users", ready: "添加用户", title: "用户管理" },
  { slug: "teams", path: "/admin/teams", ready: "创建团队", title: "团队管理" },
  { slug: "runtime", path: "/admin/runtime", ready: "容器服务", title: "运行维护" },
  { slug: "configuration", path: "/admin/configuration", ready: "保存配置", title: "配置中心" },
  { slug: "setup", path: "/admin/setup", ready: "完成基础配置", title: "首次设置" }
] as const;

const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "narrow", width: 1024, height: 768 },
  { name: "mobile", width: 390, height: 844 }
] as const;

test("管理登录表单下移后保持完整、均衡的卡片布局", async ({ page }) => {
  for (const viewport of [viewports[0], viewports[2]]) {
    await page.setViewportSize(viewport);
    await page.goto("/admin/overview");
    const geometry = await page.locator(".admin-login-layout .auth-card").evaluate((card) => {
      const eyebrow = card.querySelector<HTMLElement>(":scope > .eyebrow");
      const form = card.querySelector<HTMLElement>(".auth-form");
      const passwordRow = card.querySelector<HTMLElement>(".password-row");
      const quietLink = card.querySelector<HTMLElement>(".quiet-link");
      if (!eyebrow || !form || !passwordRow || !quietLink) throw new Error("管理登录布局锚点缺失");
      const cardRect = card.getBoundingClientRect();
      const eyebrowRect = eyebrow.getBoundingClientRect();
      const formRect = form.getBoundingClientRect();
      const passwordRowRect = passwordRow.getBoundingClientRect();
      const quietLinkRect = quietLink.getBoundingClientRect();
      return {
        formGap: formRect.top - eyebrowRect.bottom,
        passwordRowInsideForm: passwordRowRect.top >= formRect.top && passwordRowRect.bottom <= formRect.bottom,
        linkInsideCard: quietLinkRect.bottom <= cardRect.bottom,
        cardInsideViewport: cardRect.top >= 0 && cardRect.bottom <= window.innerHeight
      };
    });
    expect(geometry.formGap).toBeGreaterThanOrEqual(27);
    expect(geometry.formGap).toBeLessThanOrEqual(29);
    expect(geometry.passwordRowInsideForm).toBe(true);
    expect(geometry.linkInsideCard).toBe(true);
    expect(geometry.cardInsideViewport).toBe(true);
  }
});

for (const viewport of [viewports[0], viewports[2]]) {
  test(`系统时区在首次设置与配置中心共享选择 ${viewport.name}`, async ({ page }, testInfo) => {
    let timezone = "Asia/Shanghai";
    let selected = false;
    const saved: string[] = [];
    await page.setViewportSize(viewport);
    await page.route("**/site-config.json", async (route) => {
      const response = await route.fetch();
      await route.fulfill({ response, json: { ...await response.json(), timezone } });
    });
    await page.route("**/admin/api/settings/configuration", async (route) => {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        expect(Object.keys(body.values)).toEqual(["system.timezone"]);
        timezone = body.values["system.timezone"];
        saved.push(timezone);
        selected = true;
        await route.fulfill({ json: { message: "已保存系统时区", changed: ["system.timezone"], applied: ["collector"], pending_deployment: false } });
        return;
      }
      const response = await route.fetch();
      const payload = await response.json();
      for (const group of payload.groups) {
        for (const field of group.fields) if (field.key === "system.timezone") field.value = timezone;
      }
      await route.fulfill({ response, json: payload });
    });
    await page.route("**/admin/api/onboarding", async (route) => {
      const response = await route.fetch();
      const payload = await response.json();
      payload.steps.find((step: { id: string }) => step.id === "quota_timezone").status = selected ? "complete" : "incomplete";
      payload.recommended.complete = payload.recommended.total - (selected ? 0 : 1);
      await route.fulfill({ response, json: payload });
    });
    await login(page, "/admin/overview");
    await page.goto("/admin/setup?step=quota_timezone");
    const selector = page.getByRole("combobox", { name: "系统时区" });
    await selector.click();
    await selector.fill("America/New_York");
    await expect(page.getByText(/纽约 · America\/New_York/).last()).toBeVisible();
    const dropdown = await page.locator(".ant-select-dropdown:visible").boundingBox();
    expect(dropdown).not.toBeNull();
    expect(dropdown!.x).toBeGreaterThanOrEqual(0);
    expect(dropdown!.x + dropdown!.width).toBeLessThanOrEqual(viewport.width);
    await page.screenshot({ path: testInfo.outputPath(`setup-timezone-${viewport.name}.png`), animations: "disabled" });
    await selector.press("Enter");
    await page.getByRole("button", { name: "保存时区", exact: true }).click();
    await expect.poll(() => saved).toEqual(["America/New_York"]);
    await expect(page).toHaveURL(/step=weekly_quota/);

    await page.goto("/admin/configuration?group=系统设置");
    await expect(page.locator(".configuration-field-value .ant-select")).toContainText("America/New_York");
    await selector.click();
    await selector.fill("协调世界时");
    await selector.press("Enter");
    await page.getByRole("button", { name: "保存配置", exact: true }).click();
    await page.getByRole("button", { name: "保存并应用", exact: true }).click();
    await expect.poll(() => saved).toEqual(["America/New_York", "UTC"]);
    await expect(page.locator(".configuration-field-value .ant-select")).toContainText("UTC");

    // The browser context still uses Shanghai. A UTC site must display the
    // collector timestamp in UTC after reopening another surface.
    const overviewResponse = page.waitForResponse((response) => response.url().includes("/admin/api/overview/usage?") && response.status() === 200);
    await page.goto("/admin/overview");
    const overview = await (await overviewResponse).json();
    const expectedTime = new Intl.DateTimeFormat("zh-CN", {
      timeZone: "UTC", year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false
    }).format(new Date(overview.collector.heartbeat_at * 1000));
    await expect(page.getByLabel("最近采集时间")).toContainText(expectedTime);
    // Finish fixture reads before the test closes its browser context.
    await page.unrouteAll({ behavior: "wait" });
  });
}

for (const viewport of viewports) {
  for (const theme of ["light", "dark"] as const) {
    test(`React 页面矩阵 ${viewport.name} ${theme}`, async ({ page }) => {
      test.setTimeout(120_000);
      await page.clock.setFixedTime(new Date("2026-08-28T05:42:00.000Z"));
      await page.setViewportSize(viewport);
      await setTheme(page, theme);
      await login(page, routes[0].path);

      for (const route of routes) {
        await openRoute(page, route);
        if (viewport.width <= 560 && route.slug !== "setup") await expectActiveNavigationVisible(page);
        if (route.slug === "setup" && viewport.width <= 560) {
          const setupSteps = page.locator(".onboarding-steps");
          const lastStep = page.getByRole("navigation", { name: "初始化配置" }).getByRole("button").last();
          await lastStep.scrollIntoViewIfNeeded();
          const stepReachability = await setupSteps.evaluate((element) => {
            const last = element.querySelector<HTMLElement>(".onboarding-step-group:last-child button:last-child");
            const viewport = element.getBoundingClientRect();
            const target = last?.getBoundingClientRect();
            return {
              scrolled: element.scrollLeft > 0,
              targetVisible: Boolean(target && target.left >= viewport.left && target.right <= viewport.right)
            };
          });
          expect(stepReachability).toEqual({ scrolled: true, targetVisible: true });
          await setupSteps.evaluate((element) => element.scrollTo({ left: 0 }));
          const geometry = await page.evaluate(() => {
            const hero = document.querySelector<HTMLElement>(".onboarding-hero");
            return {
              viewport: window.innerWidth,
              body: document.body.scrollWidth,
              main: document.querySelector<HTMLElement>(".main-surface")?.scrollWidth ?? 0,
              shellWidth: document.querySelector<HTMLElement>(".onboarding-shell")?.getBoundingClientRect().width ?? 0,
              heroRight: hero?.getBoundingClientRect().right ?? 0
            };
          });
          expect(geometry.body).toBeLessThanOrEqual(geometry.viewport);
          expect(geometry.main).toBeLessThanOrEqual(geometry.viewport);
          expect(geometry.shellWidth).toBeLessThanOrEqual(geometry.viewport - 24);
          expect(geometry.heroRight).toBeLessThanOrEqual(geometry.viewport - 12);
        }
        if (route.slug === "setup") {
          await expect(page.getByRole("navigation", { name: "初始化配置" })).toBeVisible();
        }
        if (route.slug === "configuration") {
          const configurationPanel = page.getByRole("region", { name: "站点品牌", exact: true });
          await expect(configurationPanel.getByLabel("产品名称", { exact: true })).toBeVisible();
          await expect(page.getByRole("heading", { level: 1 })).toHaveText("配置中心/品牌与身份/站点品牌");
          expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
        }
        await screenshotExpect(page).toHaveScreenshot(
          `react-${route.slug}-${viewport.name}-${theme}.png`,
          {
            fullPage: false,
            // macOS 15 and 27 rasterize mobile glyph edges differently. A
            // slightly wider color delta filters that antialiasing noise while
            // the bounded pixel ratio and the separate navigation, geometry
            // and overflow assertions continue to catch structural regressions.
            threshold: route.slug === "configuration" ? 0.15 : viewport.width <= 560 ? 0.3 : 0.2,
            maxDiffPixelRatio:
              route.slug === "configuration" ? 0.005 : viewport.width <= 560
                ? 0.02
                : viewport.width <= 1024
                  ? 0.015
                  : route.slug === "setup"
                    ? 0.006
                    : 0.005
          }
        );
      }
    });
  }
}

test("用户时间筛选在窄屏不挤没列表，用户行与分页均可到达", async ({ page }) => {
  await setTheme(page, "light");
  await login(page, "/admin/users", "添加用户");
  for (const viewport of [viewports[1], viewports[2]]) {
    await page.setViewportSize(viewport);
    const row = page.locator(".user-summary-row").first();
    const tableBody = page.locator(".user-legacy-table .ant-table-body");
    await row.scrollIntoViewIfNeeded();
    await expect(row).toBeInViewport();
    expect(await tableBody.evaluate((element) => element.clientHeight)).toBeGreaterThanOrEqual(120);
    await row.locator(".table-primary").click();
    await expect(row).toHaveAttribute("aria-expanded", "true");
    await row.locator(".table-primary").click();
    await expect(row).toHaveAttribute("aria-expanded", "false");
    const pagination = page.getByRole("navigation", { name: "用户列表页码" });
    await pagination.scrollIntoViewIfNeeded();
    await expect(pagination).toBeInViewport();
    expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
  }
});

test("首次管理登录进入独立配置页，状态接口失败时不阻塞其他管理页面", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await page.route("**/admin/api/onboarding", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.required_complete = false;
    payload.required = { complete: 0, total: 2 };
    payload.recommended = { complete: 0, skipped: 0, total: 6 };
    payload.steps = payload.steps
      .filter((step: { id: string }) => !["first_account", "account_authorization", "first_user"].includes(step.id))
      .map((step: { status: string }) => ({
      ...step,
      status: "incomplete"
      }));
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/overview");
  await expect(page).toHaveURL(/\/admin\/setup/);
  await expect(page.getByRole("heading", { name: "完成基础配置" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "初始化配置" })).toBeVisible();
  await expect(page.getByText("必须完成", { exact: true })).toHaveCount(0);
  await expect(page.getByText("推荐设置", { exact: true })).toHaveCount(0);
  await expect(page.getByText("首个 CPA", { exact: true })).toHaveCount(0);
  await expect(page.getByText("OAuth 授权", { exact: true })).toHaveCount(0);
  await expect(page.getByText("首个用户", { exact: true })).toHaveCount(0);
  await expect(page.locator(".onboarding-bottom-bar")).toHaveCount(0);
  await expect(page.getByLabel("允许的邮箱域名")).toBeVisible();
  const setupGeometry = await page.evaluate(() => {
    const main = document.querySelector<HTMLElement>(".main-surface");
    const workspace = document.querySelector<HTMLElement>(".onboarding-workspace");
    const steps = document.querySelector<HTMLElement>(".onboarding-steps");
    const panel = document.querySelector<HTMLElement>(".onboarding-step-panel");
    const form = document.querySelector<HTMLElement>(".onboarding-inline-form");
    const field = document.querySelector<HTMLElement>("#onboarding-email-domains");
    const help = form?.querySelector<HTMLElement>("small") ?? null;
    const submit = form?.querySelector<HTMLElement>(".ant-btn") ?? null;
    const rect = (element: HTMLElement | null) => element?.getBoundingClientRect();
    return {
      mainClientHeight: main?.clientHeight ?? 0,
      mainScrollHeight: main?.scrollHeight ?? 0,
      workspaceClientHeight: workspace?.clientHeight ?? 0,
      workspaceScrollHeight: workspace?.scrollHeight ?? 0,
      stepsBottom: rect(steps)?.bottom ?? 0,
      panelTop: rect(panel)?.top ?? 0,
      panelWidth: rect(panel)?.width ?? 0,
      formWidth: rect(form)?.width ?? 0,
      formRight: rect(form)?.right ?? 0,
      fieldWidth: rect(field)?.width ?? 0,
      fieldBottom: rect(field)?.bottom ?? 0,
      helpBottom: rect(help)?.bottom ?? 0,
      submitTop: rect(submit)?.top ?? 0,
      submitRight: rect(submit)?.right ?? 0
    };
  });
  expect(setupGeometry.mainScrollHeight).toBeLessThanOrEqual(setupGeometry.mainClientHeight + 1);
  expect(setupGeometry.workspaceScrollHeight).toBeLessThanOrEqual(setupGeometry.workspaceClientHeight + 1);
  expect(setupGeometry.stepsBottom).toBeLessThanOrEqual(setupGeometry.panelTop + 1);
  expect(setupGeometry.formWidth).toBeGreaterThan(setupGeometry.panelWidth * 0.9);
  expect(setupGeometry.fieldWidth).toBeGreaterThan(setupGeometry.formWidth * 0.9);
  expect(setupGeometry.submitTop).toBeGreaterThanOrEqual(setupGeometry.fieldBottom);
  expect(setupGeometry.submitTop).toBeGreaterThanOrEqual(setupGeometry.helpBottom);
  expect(setupGeometry.formRight - setupGeometry.submitRight).toBeLessThanOrEqual(24);
  const configurationNavigation = page.getByRole("navigation", { name: "初始化配置" });
  await configurationNavigation.getByRole("button", { name: /访问地址/ }).click();
  await expect(page.getByLabel("公开访问地址")).toBeVisible();
  const publicURLGeometry = await page.locator(".onboarding-inline-form").evaluate((form) => {
    const field = form.querySelector<HTMLElement>("#onboarding-public-url");
    const help = form.querySelector<HTMLElement>("small");
    const submit = form.querySelector<HTMLElement>(".ant-btn");
    const formRect = form.getBoundingClientRect();
    const fieldRect = field?.getBoundingClientRect();
    const helpRect = help?.getBoundingClientRect();
    const submitRect = submit?.getBoundingClientRect();
    return {
      fieldBottom: fieldRect?.bottom ?? 0,
      fieldWidth: fieldRect?.width ?? 0,
      formWidth: formRect.width,
      helpBottom: helpRect?.bottom ?? 0,
      submitRightGap: formRect.right - (submitRect?.right ?? 0),
      submitTop: submitRect?.top ?? 0
    };
  });
  expect(publicURLGeometry.fieldWidth).toBeGreaterThan(publicURLGeometry.formWidth * 0.9);
  expect(publicURLGeometry.submitTop).toBeGreaterThanOrEqual(publicURLGeometry.fieldBottom);
  expect(publicURLGeometry.submitTop).toBeGreaterThanOrEqual(publicURLGeometry.helpBottom);
  expect(publicURLGeometry.submitRightGap).toBeLessThanOrEqual(24);
  for (const recommendation of [
    { step: /系统时区/, field: "系统时区" },
    { step: /默认额度/, field: "新用户默认周额度" },
    { step: /^通知/, field: "企业微信群 Webhook" },
    { step: /^品牌/, field: "产品名称" },
    { step: /上游代理/, field: "默认上游代理 URL" }
  ]) {
    await configurationNavigation.getByRole("button", { name: recommendation.step }).click();
    await expect(page.getByLabel(recommendation.field)).toBeVisible();
    await expect(page.locator(".onboarding-inline-form")).toBeVisible();
    await expect(page.getByRole("button", { name: "前往设置" })).toHaveCount(0);
  }
  await configurationNavigation.getByRole("button", { name: /^品牌/ }).click();
  const brandingGeometry = await page.locator(".onboarding-inline-form").evaluate((form) => {
    const fields = form.querySelector<HTMLElement>(".onboarding-form-fields");
    const submit = form.querySelector<HTMLElement>(".ant-btn");
    const formRect = form.getBoundingClientRect();
    const fieldsRect = fields?.getBoundingClientRect();
    const submitRect = submit?.getBoundingClientRect();
    return {
      fieldsBottom: fieldsRect?.bottom ?? 0,
      submitRightGap: formRect.right - (submitRect?.right ?? 0),
      submitTop: submitRect?.top ?? 0
    };
  });
  expect(brandingGeometry.submitTop).toBeGreaterThanOrEqual(brandingGeometry.fieldsBottom);
  expect(brandingGeometry.submitRightGap).toBeLessThanOrEqual(24);
  await configurationNavigation.getByRole("button", { name: /初始密码/ }).click();
  await page.setViewportSize({ width: 1024, height: 768 });
  const narrowGeometry = await page.evaluate(() => {
    const main = document.querySelector<HTMLElement>(".main-surface");
    const workspace = document.querySelector<HTMLElement>(".onboarding-workspace");
    return {
      mainClientHeight: main?.clientHeight ?? 0,
      mainScrollHeight: main?.scrollHeight ?? 0,
      workspaceClientHeight: workspace?.clientHeight ?? 0,
      workspaceScrollHeight: workspace?.scrollHeight ?? 0
    };
  });
  expect(narrowGeometry.mainScrollHeight).toBeLessThanOrEqual(narrowGeometry.mainClientHeight + 1);
  expect(narrowGeometry.workspaceScrollHeight).toBeLessThanOrEqual(narrowGeometry.workspaceClientHeight + 1);
  await page.goto("/admin/setup?step=branding");
  await expect(page.getByLabel("产品名称")).toBeVisible();
  const narrowBrandingGeometry = await page.evaluate(() => {
    const main = document.querySelector<HTMLElement>(".main-surface");
    const workspace = document.querySelector<HTMLElement>(".onboarding-workspace");
    const form = document.querySelector<HTMLElement>(".onboarding-inline-form");
    const submit = form?.querySelector<HTMLElement>(".ant-btn");
    const formRect = form?.getBoundingClientRect();
    const submitRect = submit?.getBoundingClientRect();
    return {
      mainClientHeight: main?.clientHeight ?? 0,
      mainScrollHeight: main?.scrollHeight ?? 0,
      workspaceClientHeight: workspace?.clientHeight ?? 0,
      workspaceScrollHeight: workspace?.scrollHeight ?? 0,
      submitRightGap: (formRect?.right ?? 0) - (submitRect?.right ?? 0)
    };
  });
  expect(narrowBrandingGeometry.mainScrollHeight).toBeLessThanOrEqual(narrowBrandingGeometry.mainClientHeight + 1);
  expect(narrowBrandingGeometry.workspaceScrollHeight).toBeLessThanOrEqual(narrowBrandingGeometry.workspaceClientHeight + 1);
  expect(narrowBrandingGeometry.submitRightGap).toBeLessThanOrEqual(24);
  await page.unroute("**/admin/api/onboarding");
  await page.route("**/admin/api/onboarding", (route) => route.fulfill({
    status: 503,
    contentType: "application/json",
    body: JSON.stringify({ error: { code: "status_unavailable", message: "状态检查暂不可用" } })
  }));
  await page.goto("/admin/accounts");
  await expect(page).toHaveURL(/\/admin\/accounts/);
  await expect(page.getByText("更新通道", { exact: false }).first()).toBeVisible();
});

for (const viewport of [viewports[0], viewports[1], viewports[2]]) {
  test(`配置中心七类导航保留全部字段、草稿与固定保存栏 ${viewport.name}`, async ({ page }, testInfo) => {
    test.setTimeout(90_000);
    await page.setViewportSize(viewport);
    await setTheme(page, "dark");
    await login(page, "/admin/configuration", "保存配置");
    const response = await page.request.get("/admin/api/settings/configuration");
    const catalog = await response.json();
    // The legacy scope key remains API-compatible; account enrollment moved to Account Management.
    const expectedKeys = catalog.groups.flatMap((group: { fields: Array<{ key: string }> }) => group.fields.map((field) => field.key)).filter((key: string) => key !== "plugins.codex_ticket.accounts");
    const seen: string[] = [];
    const categories = ["品牌与身份", "系统设置", "CPA 容器", "请求与账号", "用量与额度", "通知设置", "数据与审计"];
    const expandCategory = async (category: string) => {
      if (viewport.width <= 1120) {
        const toggle = page.getByRole("button", { name: "选择配置子项", exact: true });
        if (await toggle.getAttribute("aria-expanded") !== "true") await toggle.click();
      }
      const categoryButton = page.getByRole("navigation", { name: "配置分类" }).getByRole("button", { name: category, exact: true });
      if (await categoryButton.getAttribute("aria-expanded") !== "true") await categoryButton.click();
      return page.locator(`[id="${await categoryButton.getAttribute("aria-controls")}"]`);
    };
    const selectSection = async (category: string, section: string) => {
      const children = await expandCategory(category);
      await children.getByRole("button", { name: section, exact: true }).click();
    };
    for (const category of categories) {
      const children = await expandCategory(category);
      const sections = await children.getByRole("button").evaluateAll((buttons) => buttons.map((button) => button.getAttribute("aria-label")!));
      for (const section of sections) {
        await selectSection(category, section);
        await expect(page.getByRole("heading", { level: 1 })).toHaveText(`配置中心/${category}/${section}`);
        await expect(page.locator(".configuration-section")).toHaveCount(1);
        await expect(page.locator(".configuration-category-intro, .configuration-section-heading")).toHaveCount(0);
        if (section === "Codex Ticket 插件") await page.locator(".ticket-advanced > summary").click();
        const keys = await page.locator("[data-configuration-field]").evaluateAll((elements) => elements.map((element) => (element as HTMLElement).dataset.configurationField!));
        seen.push(...keys.filter((key) => expectedKeys.includes(key)));
        for (const key of keys.filter((key) => expectedKeys.includes(key))) {
          await expect(page.locator(`[data-configuration-field="${key}"]`).locator("input, button, select").first()).toBeVisible();
        }
        expect(await page.evaluate(() => document.body.scrollWidth)).toBeLessThanOrEqual(viewport.width);
        const geometry = await page.locator(".configuration-save-region").boundingBox();
        if (section === "Codex Ticket 插件") expect(geometry).toBeNull();
        else expect(geometry!.y + geometry!.height).toBeLessThanOrEqual(viewport.height);
      }
      await page.screenshot({ path: testInfo.outputPath(`configuration-${categories.indexOf(category)}-${viewport.name}.png`), animations: "disabled" });
    }
    expect(seen.sort()).toEqual(expectedKeys.sort());
    await selectSection("品牌与身份", "站点品牌");
    await page.getByLabel("产品名称", { exact: true }).fill("Draft Brand");
    await page.getByLabel("搜索配置", { exact: true }).fill("portal.session_ttl_seconds");
    await page.getByLabel("搜索配置", { exact: true }).press("Enter");
    const ttl = page.locator('[data-configuration-field="portal.session_ttl_seconds"] input');
    await expect(ttl).toBeFocused();
    await ttl.fill("1");
    await expect(page.getByText("涉及 2 个分类", { exact: true })).toBeVisible();
    // Category and child labels remain stable when their dirty counts change.
    await selectSection("品牌与身份", "站点品牌");
    await expect(page.getByLabel("产品名称", { exact: true })).toHaveValue("Draft Brand");
    await page.getByRole("button", { name: "撤销未保存修改", exact: true }).click();
    await expect(page.getByRole("button", { name: "保存配置", exact: true })).toBeDisabled();
    await page.getByLabel("搜索配置", { exact: true }).fill("no-such-configuration");
    await expect(page.getByText("没有匹配项", { exact: true })).toBeVisible();
  });
}

test("配置中心本地数据与审计记录沿用统一信息卡片", async ({ page }) => {
  await page.clock.setFixedTime(new Date("2026-08-28T05:42:00.000Z"));
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await page.route("**/admin/api/settings/workspace", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.storage = [
      { label: "控制面数据库", path: "state/control-plane.sqlite3", exists: true, mode: "600" },
      { label: "用户用量数据库", path: "state/usage.sqlite3", exists: true, mode: "600" },
      { label: "控制面加密主密钥", path: "secrets/control-plane.key", exists: true, mode: "600" },
      { label: "管理操作审计", path: "logs/admin/audit.jsonl", exists: false, mode: "—" }
    ];
    payload.recent_audit = [];
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/configuration", "保存配置");

  const systemNavigation = page.getByRole("navigation", { name: "配置分类" });
  await expect(systemNavigation.getByRole("button", { name: "数据与审计" })).toHaveAttribute("aria-expanded", "true");
  const storageButton = page.getByRole("button", { name: /本地数据/ });
  await storageButton.click();
  await expect(storageButton).toHaveAttribute("aria-current", "page");
  await expect(page.locator(".configuration-navigation .active")).toHaveCount(1);
  await expect(systemNavigation.locator(".active")).toHaveCount(1);
  await expect(page.getByRole("region", { name: "持久化数据" })).toBeVisible();
  await expect(page.getByText("用户用量数据库", { exact: true })).toBeVisible();
  const storageRows = page.getByLabel("存储状态表格").getByRole("row");
  await expect(storageRows).toHaveCount(5);
  const storageGeometry = await page.getByLabel("存储状态表格").evaluate((element) => {
    const viewport = element.getBoundingClientRect();
    const table = element.querySelector("table")?.getBoundingClientRect();
    const lastRow = element.querySelector("tbody tr:last-child")?.getBoundingClientRect();
    return { viewportHeight: viewport.height, tableHeight: table?.height ?? 0, lastRowBottom: lastRow?.bottom ?? 0, viewportBottom: viewport.bottom };
  });
  expect(storageGeometry.tableHeight).toBeGreaterThan(200);
  expect(storageGeometry.viewportHeight).toBeGreaterThan(200);
  expect(storageGeometry.lastRowBottom).toBeLessThanOrEqual(storageGeometry.viewportBottom + 1);
  await screenshotExpect(page).toHaveScreenshot("react-configuration-storage-desktop-dark.png", { fullPage: false });

  await page.setViewportSize({ width: 390, height: 844 });
  const mobileStorageGeometry = await page.locator(".settings-workspace-content").evaluate((element) => ({
    clientWidth: element.clientWidth,
    scrollWidth: element.scrollWidth,
    bodyScrollWidth: document.body.scrollWidth,
    viewportWidth: window.innerWidth
  }));
  expect(mobileStorageGeometry.scrollWidth).toBeGreaterThan(mobileStorageGeometry.clientWidth);
  expect(mobileStorageGeometry.bodyScrollWidth).toBeLessThanOrEqual(mobileStorageGeometry.viewportWidth);
  await screenshotExpect(page).toHaveScreenshot("react-configuration-storage-mobile-dark.png", {
    fullPage: false,
    threshold: 0.3,
    maxDiffPixelRatio: 0.02
  });

  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(systemNavigation).toBeVisible();

  const auditButton = systemNavigation.getByRole("button", { name: "审计记录", exact: true });
  await auditButton.click();
  await expect(auditButton).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("heading", { name: "暂无管理操作" })).toBeVisible();
  await expect(page.getByRole("button", { name: "刷新审计记录" })).toBeVisible();
  await screenshotExpect(page).toHaveScreenshot("react-configuration-audit-empty-desktop-dark.png", { fullPage: false });

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("heading", { name: "暂无管理操作" })).toBeVisible();
  await screenshotExpect(page).toHaveScreenshot("react-configuration-audit-empty-mobile-dark.png", {
    fullPage: false,
    threshold: 0.3,
    maxDiffPixelRatio: 0.02
  });
});

for (const viewport of viewports) {
  for (const theme of ["light", "dark"] as const) {
    test(`React 使用中心 ${viewport.name} ${theme} 视觉基准`, async ({ browser }) => {
      const context = await browser.newContext({
        baseURL: "http://127.0.0.1:5194",
        viewport,
        locale: "zh-CN",
        timezoneId: "Asia/Shanghai",
        colorScheme: theme
      });
      await context.addInitScript((value) => localStorage.setItem("cpa-ui-theme", value), theme);
      await context.addCookies([{ name: "cpa-ui-language", value: "zh-CN", url: "http://127.0.0.1" }]);
      const page = await context.newPage();
      await installUsageVisualBackend(page);
      await page.goto("/usage/");
      await expect(page.getByRole("tab", { name: "账号明细" })).toHaveAttribute("aria-selected", "true");
      await expect(page.getByRole("tab", { name: "每日用量" })).toHaveAttribute("aria-selected", "false");
      await screenshotExpect(page).toHaveScreenshot(`react-usage-${viewport.name}-${theme}.png`, {
        fullPage: false,
        // Keep mobile baselines resilient to macOS glyph antialiasing drift.
        // Interaction, scrolling and layout geometry are asserted separately.
        threshold: viewport.width <= 560 ? 0.3 : 0.2,
        maxDiffPixelRatio: viewport.width <= 560 ? 0.02 : 0.005
      });
      await context.close();
    });
  }
}

for (const state of ["loading", "empty", "error"] as const) {
  test(`React 使用中心 ${state} 视觉状态`, async ({ browser }) => {
    const context = await browser.newContext({
      baseURL: "http://127.0.0.1:5194",
      viewport: { width: 1440, height: 900 },
      locale: "zh-CN",
      timezoneId: "Asia/Shanghai"
    });
    await context.addCookies([{ name: "cpa-ui-language", value: "zh-CN", url: "http://127.0.0.1" }]);
    const page = await context.newPage();
    await installUsageVisualBackend(page, state);
    await page.goto("/usage/");
    await page.getByRole("tab", { name: "账号明细" }).click();
    if (state === "loading") await expect(page.locator(".usage-skeleton-row").first()).toBeVisible();
    if (state === "empty") await expect(page.getByText("暂无可用账号", { exact: true })).toBeVisible();
    if (state === "error") await expect(page.getByText("账号与用量加载失败", { exact: true })).toBeVisible();
    await screenshotExpect(page).toHaveScreenshot(`react-usage-state-${state}.png`, { fullPage: false });
    await context.close();
  });
}

test("个人使用中心账号明细默认展开，按需加载趋势并保留双页签状态", async ({ page }) => {
  await page.setViewportSize({ width: 1086, height: 900 });
  await setTheme(page, "light");
  const trendRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/usage/me/usage-trend") trendRequests.push(`${url.pathname}?${url.searchParams.toString()}`);
  });
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  const chart = page.getByRole("img", { name: /个人每日 Token 用量趋势/ });
  const sectionTabs = page.locator(".usage-primary-tabs [role=tab]");
  await expect(sectionTabs).toHaveCount(2);
  await expect(sectionTabs.nth(0)).toContainText("账号明细");
  await expect(sectionTabs.nth(1)).toHaveText("每日用量");
  await expect(page.getByRole("tab", { name: "账号明细" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tab", { name: "每日用量" })).toHaveAttribute("aria-selected", "false");
  await expect(page.getByRole("columnheader", { name: /CPA 账号/ })).toBeVisible();
  await expect(page.locator(".usage-account-table thead th").first()).toHaveCSS("position", "sticky");
  await expect(page.locator(".usage-last-used").first()).toHaveText(/^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}$/);
  await expect(chart).toHaveCount(0);
  await expect.poll(() => trendRequests).toEqual([]);

  const topCardRect = await page.locator(".usage-key-card").evaluate((element) => element.getBoundingClientRect().toJSON());
  const detailFrameRect = await page.locator(".usage-detail-sections").evaluate((element) => element.getBoundingClientRect().toJSON());
  expect(Math.abs(topCardRect.left - detailFrameRect.left)).toBeLessThanOrEqual(1);
  expect(Math.abs(topCardRect.right - detailFrameRect.right)).toBeLessThanOrEqual(1);
  const quotaHelp = page.getByRole("button", { name: "查看个人本周额度 Token 说明" });
  await quotaHelp.hover();
  const quotaTooltip = page.locator(".usage-quota-tooltip");
  await expect(quotaTooltip).toBeVisible();
  const quotaTooltipText = await quotaTooltip.innerText();
  expect(quotaTooltipText).toMatch(/\d{1,3}(?:,\d{3})+/);
  expect(quotaTooltipText).not.toMatch(/\b(?:Token|[KMB])\b/);
  await page.mouse.move(0, 0);
  await expect(quotaTooltip).toBeHidden();
  const summaryTags = await page.locator(".usage-current-account-head .usage-summary-tag, .usage-personal-overview-head .usage-summary-tag").evaluateAll((tags) => tags.map((tag) => {
    const style = getComputedStyle(tag);
    const rect = tag.getBoundingClientRect();
    return {
      height: rect.height,
      fontSize: style.fontSize,
      lineHeight: style.lineHeight,
      paddingTop: style.paddingTop,
      paddingBottom: style.paddingBottom,
      borderRadius: style.borderRadius,
      alignItems: style.alignItems
    };
  }));
  expect(summaryTags).toHaveLength(2);
  expect(summaryTags[0]).toEqual(summaryTags[1]);
  const sectionTabRects = await sectionTabs.evaluateAll((tabs) => tabs.map((tab) => {
    const rect = tab.getBoundingClientRect();
    return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right };
  }));
  expect(sectionTabRects).toHaveLength(2);
  expect(Math.abs(sectionTabRects[0].top - sectionTabRects[1].top)).toBeLessThanOrEqual(1);
  expect(Math.abs(sectionTabRects[0].bottom - sectionTabRects[1].bottom)).toBeLessThanOrEqual(1);
  expect(sectionTabRects[1].left).toBeGreaterThanOrEqual(sectionTabRects[0].right);
  expect(sectionTabRects.every((tab) => tab.left >= detailFrameRect.left && tab.right <= detailFrameRect.right)).toBe(true);
  await expect(page.locator(".usage-section-switcher")).toHaveCount(0);

  const initialAccountActions = page.locator(".usage-mobile-panel-actions.usage-tab-toolbar-actions");
  await expect(initialAccountActions).toBeVisible();
  await page.getByRole("tab", { name: "每日用量" }).click();
  await expect(chart).toBeVisible();
  const chartCanvasRect = await page.locator(".usage-trend-chart-canvas").evaluate((element) => element.getBoundingClientRect().toJSON());
  expect(chartCanvasRect.height).toBeGreaterThan(300);
  expect(detailFrameRect.bottom - chartCanvasRect.bottom).toBeGreaterThanOrEqual(0);
  expect(detailFrameRect.bottom - chartCanvasRect.bottom).toBeLessThanOrEqual(16);
  const trendTabActions = page.locator(".usage-mobile-panel-actions.usage-trend-toolbar-actions");
  await expect(trendTabActions).toBeVisible();
  expect(await trendTabActions.evaluate((element) => {
    const action = element.getBoundingClientRect();
    const section = element.closest(".usage-detail-sections");
    const navigation = section?.querySelector(".ant-tabs-nav")?.getBoundingClientRect();
    const frame = section?.getBoundingClientRect();
    return Boolean(navigation && frame && action.top >= navigation.bottom && action.bottom <= frame.bottom && action.right <= frame.right);
  })).toBe(true);
  await expect.poll(() => trendRequests).toEqual([
    "/usage/me/usage-trend?window=30d&dimension=total"
  ]);
  await expect(page.getByRole("tab", { name: "每日用量" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tab", { name: "账号明细" })).toHaveAttribute("aria-selected", "false");
  await expect(page.getByRole("button", { name: "展开", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "收起", exact: true })).toHaveCount(0);
  await expect(page.getByRole("columnheader", { name: /CPA 账号/ })).toHaveCount(0);
  await expect(page.getByText("浅色缺口表示该自然日尚未采集或只采集了部分时段。")).toHaveCount(0);
  const trendSummary = page.getByLabel("趋势摘要");
  await expect(trendSummary.getByText("30天用量", { exact: true })).toBeVisible();
  await expect(trendSummary.getByText("未加权", { exact: true })).toHaveCount(3);
  await expect(trendSummary.getByText("加权", { exact: true })).toHaveCount(3);
  const summaryMetricLayout = await trendSummary.locator(".usage-trend-summary-value").evaluateAll((rows) => rows.map((row) => {
    const marker = row.querySelector<HTMLElement>(".usage-trend-summary-value-marker");
    const label = row.querySelector<HTMLElement>("small span");
    const card = row.closest<HTMLElement>(".has-metrics");
    const values = row.closest<HTMLElement>(".usage-trend-summary-values");
    const heading = card?.querySelector<HTMLElement>(":scope > span");
    if (!marker || !label || !card || !values || !heading) return null;
    const markerRect = marker.getBoundingClientRect();
    const labelRect = label.getBoundingClientRect();
    const cardRect = card.getBoundingClientRect();
    const valuesRect = values.getBoundingClientRect();
    const headingRect = heading.getBoundingClientRect();
    return {
      label: label.textContent,
      markerVisible: markerRect.width === 6 && markerRect.height === 6 && getComputedStyle(marker).backgroundColor !== "rgba(0, 0, 0, 0)",
      centerDelta: Math.abs((markerRect.top + markerRect.height / 2) - (labelRect.top + labelRect.height / 2)),
      valuesAtRight: valuesRect.left >= headingRect.right + 4 && valuesRect.right <= cardRect.right + 1
    };
  }));
  expect(summaryMetricLayout).toHaveLength(6);
  expect(summaryMetricLayout.every((metric) => metric?.markerVisible && metric.centerDelta <= 1 && metric.valuesAtRight)).toBe(true);
  expect(summaryMetricLayout.map((metric) => metric?.label)).toEqual([
    "加权", "未加权", "加权", "未加权", "加权", "未加权"
  ]);
  const weightedSummaryMetric = trendSummary.locator('.usage-trend-summary-value[data-metric="weighted"]').first();
  await weightedSummaryMetric.hover();
  const summaryTooltip = page.locator(".usage-trend-summary-popup [role=tooltip]");
  await expect(summaryTooltip).toBeVisible();
  await expect(summaryTooltip).toHaveText(/^\d{1,3}(?:,\d{3})+$/);
  await expect(summaryTooltip).not.toContainText("Token");
  await expect(page.getByLabel("趋势图例")).toHaveCount(0);
  await page.getByRole("button", { name: "7天", exact: true }).click();
  await page.getByRole("button", { name: "模型 + 推理强度", exact: true }).click();
  await expect.poll(() => trendRequests).toContain(
    "/usage/me/usage-trend?window=7d&dimension=model_reasoning"
  );
  await expect(page.getByText("主要组合", { exact: true })).toBeVisible();
  await expect(page.getByLabel("趋势图例")).toHaveCount(0);
  await expect(page.getByText("趋势口径", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "加权", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(trendSummary.getByText("未加权", { exact: true })).toHaveCount(2);
  await expect(trendSummary.getByText("加权", { exact: true })).toHaveCount(2);
  const metricSwitchLayout = await page.locator(".usage-trend-metric-switch button").evaluateAll((buttons) => buttons.map((button) => {
    const marker = button.querySelector<HTMLElement>("i");
    const label = button.querySelector<HTMLElement>("span");
    if (!marker || !label) return null;
    const buttonRect = button.getBoundingClientRect();
    const markerRect = marker.getBoundingClientRect();
    const labelRect = label.getBoundingClientRect();
    const contentLeft = Math.min(markerRect.left, labelRect.left);
    const contentRight = Math.max(markerRect.right, labelRect.right);
    return {
      markerColor: getComputedStyle(marker).backgroundColor,
      markerWidth: markerRect.width,
      markerHeight: markerRect.height,
      horizontalDelta: Math.abs((contentLeft + contentRight) / 2 - (buttonRect.left + buttonRect.width / 2)),
      verticalDelta: Math.abs((labelRect.top + labelRect.height / 2) - (buttonRect.top + buttonRect.height / 2))
    };
  }));
  expect(metricSwitchLayout).toHaveLength(2);
  expect(metricSwitchLayout.every((item) => item && item.markerWidth === 6 && item.markerHeight === 6
    && item.horizontalDelta <= 1 && item.verticalDelta <= 1)).toBe(true);
  expect(new Set(metricSwitchLayout.map((item) => item?.markerColor)).size).toBe(2);
  const combinationCountLayout = await page.locator(".usage-trend-summary .combination-count").evaluate((card) => {
    const label = card.querySelector<HTMLElement>(":scope > span");
    const count = card.querySelector<HTMLElement>(":scope > strong");
    if (!label || !count) return null;
    const cardRect = card.getBoundingClientRect();
    const labelRect = label.getBoundingClientRect();
    const countRect = count.getBoundingClientRect();
    return {
      count: count.textContent,
      sameLine: Math.max(labelRect.bottom, countRect.bottom) - Math.min(labelRect.top, countRect.top) <= Math.max(labelRect.height, countRect.height) + 2,
      ordered: labelRect.right <= countRect.left,
      inside: countRect.right <= cardRect.right + 1,
      clipped: count.scrollWidth > count.clientWidth
    };
  });
  expect(combinationCountLayout).toEqual(expect.objectContaining({
    count: "4",
    sameLine: true,
    ordered: true,
    inside: true,
    clipped: false
  }));
  await expect(page.getByRole("status", { name: "每日用量数据更新时间" })).toHaveText(/数据更新\s*\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}/);
  await expect(page.locator(".usage-trend-card time")).toHaveCount(0);
  const primaryCombination = page.locator(".usage-trend-summary .primary-combination > strong");
  await expect(primaryCombination).toHaveText("gpt-5.6-sol · xhigh");
  expect(await primaryCombination.evaluate((element) => ({
    clipped: element.scrollWidth > element.clientWidth,
    overflow: getComputedStyle(element).overflow,
    textOverflow: getComputedStyle(element).textOverflow
  }))).toEqual({ clipped: false, overflow: "visible", textOverflow: "clip" });
  const axisFontSizes = await page.locator(".usage-trend-chart-canvas svg text").evaluateAll((labels) => labels.map((label) => Number.parseFloat(getComputedStyle(label).fontSize)));
  expect(axisFontSizes.length).toBeGreaterThan(0);
  expect(Math.min(...axisFontSizes)).toBeGreaterThanOrEqual(10);
  expect(Math.max(...axisFontSizes)).toBeGreaterThanOrEqual(11);

  await page.getByRole("button", { name: "未加权", exact: true }).click();
  await chart.focus();
  const tooltipOuter = page.locator(".usage-trend-echarts-tooltip");
  const tooltip = page.locator(".usage-trend-tooltip[data-active=true]");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toHaveAttribute("data-layout", "single-column");
  await expect(tooltip).toContainText("当日未加权");
  expect(await tooltip.evaluate((element) => ({
    overflowX: getComputedStyle(element).overflowX,
    overflowY: getComputedStyle(element).overflowY,
    scrollable: element.scrollHeight > element.clientHeight || element.scrollWidth > element.clientWidth
  }))).toEqual({ overflowX: "hidden", overflowY: "hidden", scrollable: false });
  const combinationRows = tooltip.locator(".usage-trend-tooltip-combination");
  await expect(combinationRows.first().locator(".usage-trend-tooltip-model")).toHaveText("gpt-5.6-sol");
  await expect(combinationRows.first().locator(".usage-trend-tooltip-effort")).toHaveText("xhigh");
  await expect(combinationRows.first()).toContainText("Token");
  await expect(combinationRows.locator("small")).toHaveCount(0);
  const markerStyles = await combinationRows.locator("i").evaluateAll((markers) => markers.map((marker) => ({
    color: getComputedStyle(marker).backgroundColor,
    width: marker.getBoundingClientRect().width,
    height: marker.getBoundingClientRect().height
  })));
  expect(markerStyles.length).toBeGreaterThanOrEqual(2);
  expect(new Set(markerStyles.map((marker) => marker.color)).size).toBeGreaterThan(1);
  expect(markerStyles.every((marker) => marker.color !== "rgb(21, 27, 40)" && marker.width >= 8 && marker.height >= 8)).toBe(true);
  expect(await combinationRows.evaluateAll((rows) => rows.every((row) => {
    const labelElement = row.querySelector<HTMLElement>("b");
    const valueElement = row.querySelector<HTMLElement>("em");
    if (!labelElement || !valueElement) return false;
    const rowRect = row.getBoundingClientRect();
    const label = labelElement.getBoundingClientRect();
    const value = valueElement.getBoundingClientRect();
    return Boolean(Math.abs(label.top - value.top) <= 1
      && labelElement.clientWidth >= labelElement.scrollWidth
      && valueElement.clientWidth >= valueElement.scrollWidth
      && value.right <= rowRect.right + .5);
  }))).toBe(true);
  expect(await tooltipOuter.evaluate((outer) => {
    const outerRect = outer.getBoundingClientRect();
    const inner = outer.querySelector<HTMLElement>(".usage-trend-tooltip");
    const values = [...outer.querySelectorAll<HTMLElement>(".usage-trend-tooltip-row em, .usage-trend-tooltip-total em")];
    if (!inner) return false;
    const innerRect = inner.getBoundingClientRect();
    return innerRect.right <= outerRect.right + .5
      && values.every((value) => value.getBoundingClientRect().right <= outerRect.right + .5);
  })).toBe(true);
  const tooltipRect = await tooltip.evaluate((element) => element.getBoundingClientRect());
  expect(tooltipRect.top).toBeGreaterThanOrEqual(0);
  expect(tooltipRect.bottom).toBeLessThanOrEqual(900);
  await screenshotExpect(tooltip).toHaveScreenshot("react-usage-trend-tooltip-model-reasoning.png");
  await page.getByRole("button", { name: "加权", exact: true }).click();
  await expect(page.getByRole("button", { name: "加权", exact: true })).toHaveAttribute("aria-pressed", "true");
  expect(trendRequests.filter((path) => path.includes("dimension=model_reasoning"))).toHaveLength(1);
  await page.getByRole("tab", { name: "账号明细" }).click();
  await expect(chart).toHaveCount(0);
  await expect(page.getByRole("columnheader", { name: /CPA 账号/ })).toBeVisible();
  await expect(page.getByRole("tab", { name: "账号明细" })).toHaveAttribute("aria-selected", "true");
  const accountFrameRect = await page.locator(".usage-detail-sections").evaluate((element) => element.getBoundingClientRect().toJSON());
  expect(Math.abs(topCardRect.left - accountFrameRect.left)).toBeLessThanOrEqual(1);
  expect(Math.abs(topCardRect.right - accountFrameRect.right)).toBeLessThanOrEqual(1);
  const accountTabActions = page.locator(".usage-mobile-panel-actions.usage-tab-toolbar-actions");
  await expect(accountTabActions).toBeVisible();
  expect(await accountTabActions.evaluate((element) => {
    const action = element.getBoundingClientRect();
    const section = element.closest(".usage-detail-sections");
    const navigation = section?.querySelector(".ant-tabs-nav")?.getBoundingClientRect();
    const frame = section?.getBoundingClientRect();
    return Boolean(navigation && frame && action.top >= navigation.bottom && action.bottom <= frame.bottom && action.right <= frame.right);
  })).toBe(true);
  await page.getByRole("button", { name: "使用明细" }).click();
  const detailMetricsLayout = await page.locator(".usage-detail-panel").evaluate((panel) => {
    const heading = panel.querySelector<HTMLElement>(".usage-detail-heading");
    const cacheCell = [...panel.querySelectorAll<HTMLElement>(".usage-token-grid > div")].find((cell) => cell.querySelector("span")?.textContent?.trim() === "缓存率");
    const metricCells = [...panel.querySelectorAll<HTMLElement>(".usage-token-grid > div")].slice(0, 2);
    const tokenCells = [...panel.querySelectorAll<HTMLElement>(".usage-token-grid > div")].filter((cell) => cell.querySelector("span")?.textContent?.includes("Token"));
    if (!heading || !cacheCell || metricCells.length !== 2 || tokenCells.length !== 5) return null;
    const cacheLabel = cacheCell.querySelector<HTMLElement>("span");
    const cacheValue = cacheCell.querySelector<HTMLElement>("strong");
    if (!cacheLabel || !cacheValue) return null;
    const headingRect = heading.getBoundingClientRect();
    return {
      headingWidth: headingRect.width,
      panelWidth: panel.getBoundingClientRect().width,
      labelClipped: cacheLabel.scrollWidth > cacheLabel.clientWidth,
      cacheValue: cacheValue.textContent?.trim(),
      cacheCellText: cacheCell.textContent?.trim(),
      tokenRawValues: tokenCells.map((cell) => cell.querySelector<HTMLElement>(".usage-token-raw")?.textContent?.trim() ?? ""),
      requestAlignments: metricCells.map((cell) => ({
        textAlign: getComputedStyle(cell).textAlign,
        labelAlign: getComputedStyle(cell.querySelector("span") as HTMLElement).textAlign,
        valueAlign: getComputedStyle(cell.querySelector("strong") as HTMLElement).textAlign
      }))
    };
  });
  expect(detailMetricsLayout).not.toBeNull();
  expect(detailMetricsLayout?.headingWidth).toBeCloseTo((detailMetricsLayout?.panelWidth ?? 0) - 2, 0);
  expect(detailMetricsLayout?.labelClipped).toBe(false);
  expect(detailMetricsLayout?.cacheValue).toMatch(/%$/);
  expect(detailMetricsLayout?.cacheCellText).not.toContain("缓存 Token");
  expect(detailMetricsLayout?.tokenRawValues).toHaveLength(5);
  expect(detailMetricsLayout?.tokenRawValues?.every((value) => /^\d[\d,]* Token$/.test(value))).toBe(true);
  expect(detailMetricsLayout?.requestAlignments).toEqual([
    { textAlign: "center", labelAlign: "center", valueAlign: "center" },
    { textAlign: "center", labelAlign: "center", valueAlign: "center" }
  ]);
  const modelAlignments = await page.locator(".account-model-usage-head").evaluateAll((heads) => heads.map((head) => {
    const name = head.querySelector<HTMLElement>(".account-model-name");
    const token = head.querySelector<HTMLElement>(".account-model-token");
    const headRect = head.getBoundingClientRect();
    const nameRect = name?.getBoundingClientRect();
    const tokenRect = token?.getBoundingClientRect();
    return {
      nameAlignment: name ? getComputedStyle(name).textAlign : "missing",
      tokenAlignment: token ? getComputedStyle(token).textAlign : "missing",
      nameStartsAtLeft: Boolean(nameRect && Math.abs(nameRect.left - headRect.left) <= 1),
      tokenEndsAtRight: Boolean(tokenRect && Math.abs(tokenRect.right - headRect.right) <= 1)
    };
  }));
  expect(modelAlignments.length).toBeGreaterThan(0);
  expect(modelAlignments.every((item) => item.nameAlignment === "left" && item.nameStartsAtLeft)).toBe(true);
  expect(modelAlignments.every((item) => item.tokenAlignment === "right" && item.tokenEndsAtRight)).toBe(true);
  const compactEffort = page.getByRole("button", { name: "查看 gpt-5.6-sol max 推理强度 Token 明细" });
  await compactEffort.hover();
  const effortTooltip = page.locator(".usage-model-effort-tooltip");
  await expect(effortTooltip).toBeVisible();
  await expect(effortTooltip).toContainText("gpt-5.6-sol · max");
  await expect(effortTooltip).toContainText("该模型加权占比0.5%");
  await expect(effortTooltip).toContainText("加权 Token652,500");
  const effortPopup = page.locator(".ant-tooltip:has(.usage-model-effort-tooltip)");
  await expect(effortPopup.getByRole("tooltip")).toHaveCSS("background-color", "rgb(23, 29, 43)");
  await screenshotExpect(effortPopup).toHaveScreenshot("react-usage-model-effort-tooltip.png", {
    maxDiffPixelRatio: 0.01
  });
});

test("个人使用中心账号明细 Tab 视觉基准", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await page.getByRole("tab", { name: "账号明细" }).click();
  await page.mouse.move(0, 0);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page.locator(".ant-tooltip")).toHaveCount(0);
  await expect(page.getByRole("img", { name: /个人每日 Token 用量趋势/ })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "账号明细" })).toHaveAttribute("aria-selected", "true");
  await screenshotExpect(page).toHaveScreenshot("react-usage-trend-collapsed-desktop-light.png", { fullPage: false });
});

test("个人使用中心模型与推理强度组合趋势视觉基准", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await page.getByRole("tab", { name: "每日用量" }).click();
  await page.getByRole("button", { name: "模型 + 推理强度", exact: true }).click();
  await expect(page.getByText("主要组合", { exact: true })).toBeVisible();
  await expect(page.getByRole("img", { name: /个人每日 Token 用量趋势/ })).toBeVisible();
  await screenshotExpect(page).toHaveScreenshot("react-usage-trend-model-reasoning-desktop-light.png", { fullPage: false });

  await page.setViewportSize({ width: 390, height: 844 });
  const compactCombination = await page.locator(".usage-trend-summary .combination-count").evaluate((card) => {
    const items = [...card.children].map((child) => (child as HTMLElement).getBoundingClientRect());
    const bounds = card.getBoundingClientRect();
    return {
      sameLine: Math.max(...items.map((item) => item.bottom)) - Math.min(...items.map((item) => item.top)) <= Math.max(...items.map((item) => item.height)) + 2,
      inside: items.every((item) => item.left >= bounds.left && item.right <= bounds.right + 1),
      bodyScrollWidth: document.body.scrollWidth,
      viewportWidth: window.innerWidth
    };
  });
  expect(compactCombination.sameLine).toBe(true);
  expect(compactCombination.inside).toBe(true);
  expect(compactCombination.bodyScrollWidth).toBeLessThanOrEqual(compactCombination.viewportWidth);
});

test("个人使用中心移动端可滚动到账号明细", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");
  await page.getByRole("tab", { name: "账号明细" }).click();

  const header = page.locator(".usage-center-head");
  const content = page.locator(".usage-center-content");
  const accountSection = page.locator(".usage-account-section");
  const accountTable = page.locator(".usage-table-wrap");
  const lastAccount = accountTable.locator(".usage-summary-row").last();
  const headerTop = await header.evaluate((element) => element.getBoundingClientRect().top);

  await expect(accountSection).toBeAttached();
  expect(await accountSection.evaluate((element) => element.getBoundingClientRect().height)).toBeGreaterThan(400);
  expect(await accountTable.evaluate((element) => element.getBoundingClientRect().height)).toBeGreaterThan(0);
  await lastAccount.scrollIntoViewIfNeeded();

  expect(await content.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  expect(await header.evaluate((element) => element.getBoundingClientRect().top)).toBe(headerTop);
  const accountRect = await lastAccount.evaluate((element) => element.getBoundingClientRect());
  const viewportHeight = await page.evaluate(() => window.innerHeight);
  expect(accountRect.top).toBeGreaterThanOrEqual(0);
  expect(accountRect.bottom).toBeLessThanOrEqual(viewportHeight + 1);
  await expect(accountTable).toBeVisible();
});

test("CPA 镜像更新任务按账号展示完整结果并适配窄屏", async ({ page }) => {
  test.setTimeout(60_000);
  await page.clock.setFixedTime(new Date("2026-09-04T10:50:00.000Z"));
  await page.setViewportSize({ width: 1100, height: 820 });
  await setTheme(page, "dark");
  await page.route("**/admin/api/images/cliproxy", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.outdated_count = 2;
    payload.current_count = 1;
    payload.accounts = payload.accounts.map((account: { account: string }) => ({
      ...account,
      using_target: account.account === "cpa-edge"
    }));
    await route.fulfill({ response, json: payload });
  });
  await page.route("**/admin/api/operations", async (route) => {
    await fulfillJSON(route, {
      message: "镜像更新任务已完成",
      reused: false,
      job: {
        id: "visual-image-update",
        name: "更新全部 CPA 镜像",
        action: "image-update",
        target: "all",
        status: "succeeded",
        created_at: 1_788_490_180,
        started_at: 1_788_490_200,
        finished_at: 1_788_490_248,
        exit_code: 0,
        output: [
          "正在更新 cpa-main：sha256:9ad5f334ef30 -> sha256:d9db67a9de44",
          "cpa-main 验证通过：运行探针",
          "正在更新 cpa-lab：sha256:9ad5f334ef30 -> sha256:d9db67a9de44",
          "cpa-lab 验证通过：运行探针",
          "跳过 cpa-edge：已经运行目标镜像",
          "CPA 镜像更新完成：2 个"
        ]
      }
    });
  });
  await login(page, "/admin/accounts", "更新通道");

  await page.getByRole("button", { name: "更新全部 CPA" }).click();
  const confirmation = page.locator(".legacy-confirm-modal");
  await expect(confirmation.getByText("更新 CPA 镜像？")).toBeVisible();
  await confirmation.getByRole("button", { name: "更新全部 CPA" }).click();
  const dialog = page.locator(".legacy-output-modal");
  await expect(dialog.getByText("更新全部 CPA 镜像")).toBeVisible();
  await expect(dialog.getByLabel("任务执行摘要")).toContainText("任务耗时48 秒");
  await expect(dialog.getByLabel("镜像更新摘要")).toContainText("更新完成2");
  await expect(dialog.getByRole("list", { name: "账号更新结果" }).getByRole("listitem")).toHaveCount(3);
  await expect(dialog.locator(".image-update-raw-output")).not.toHaveAttribute("open");

  const desktopCards = await dialog.locator(".image-update-account").evaluateAll((cards) => cards.map((card) => {
    const rect = card.getBoundingClientRect();
    return { top: rect.top, left: rect.left, right: rect.right, width: rect.width };
  }));
  expect(desktopCards).toHaveLength(3);
  expect(Math.abs(desktopCards[0].top - desktopCards[1].top)).toBeLessThanOrEqual(1);
  expect(desktopCards[1].left).toBeGreaterThan(desktopCards[0].right);
  expect(desktopCards[0].width).toBeGreaterThan(300);
  await screenshotExpect(dialog).toHaveScreenshot("react-accounts-image-update-task-desktop-dark.png");

  await dialog.getByText("查看原始输出").click();
  await expect(dialog.locator(".image-update-raw-output")).toHaveAttribute("open", "");
  await expect(dialog.locator(".image-update-raw-output pre")).toContainText("CPA 镜像更新完成：2 个");

  await page.setViewportSize({ width: 390, height: 844 });
  const mobileGeometry = await dialog.evaluate((element) => {
    const cards = [...element.querySelectorAll<HTMLElement>(".image-update-account")]
      .map((card) => card.getBoundingClientRect());
    return {
      bodyScrollWidth: document.body.scrollWidth,
      viewportWidth: window.innerWidth,
      cardLefts: cards.map((card) => card.left),
      cardTops: cards.map((card) => card.top),
      cardsFit: cards.every((card) => card.left >= 0 && card.right <= window.innerWidth)
    };
  });
  expect(mobileGeometry.bodyScrollWidth).toBeLessThanOrEqual(mobileGeometry.viewportWidth);
  expect(Math.max(...mobileGeometry.cardLefts) - Math.min(...mobileGeometry.cardLefts)).toBeLessThanOrEqual(1);
  expect(new Set(mobileGeometry.cardTops).size).toBe(3);
  expect(mobileGeometry.cardsFit).toBe(true);
  await screenshotExpect(dialog).toHaveScreenshot("react-accounts-image-update-task-mobile-dark.png", {
    threshold: 0.3,
    maxDiffPixelRatio: 0.02
  });
});

test("账号展开区复用旧版四层信息结构", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await login(page, "/admin/accounts", "更新通道");

  const accountStatus = page.locator(".account-runtime-status").first();
  await accountStatus.hover();
  const statusTooltip = page.locator(".account-runtime-status-tooltip");
  await expect(statusTooltip).toBeVisible();
  expect(await statusTooltip.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return rect.top >= 0
      && rect.right <= window.innerWidth
      && element.closest(".account-legacy-table") === null;
  })).toBe(true);
  await page.mouse.move(0, 0);
  await expect(statusTooltip).toBeHidden();

  const activityHelp = page.locator(".account-activity-help").first();
  const helpText = await activityHelp.getAttribute("aria-label");
  const activityTooltip = page.getByRole("tooltip", { name: helpText! });
  await activityHelp.hover();
  await expect(activityTooltip).toBeVisible();
  expect(await activityTooltip.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return rect.top >= 0 && rect.right <= window.innerWidth
      && element.closest(".account-legacy-table") === null;
  })).toBe(true);
  await page.mouse.move(0, 0);
  await expect(activityTooltip).toBeHidden();
  await activityHelp.focus();
  await expect(activityTooltip).toBeVisible();
  await activityHelp.blur();
  await expect(activityTooltip).toBeHidden();

  await page.getByRole("row", { name: "展开 cpa-main" }).click();
  await expect(page.getByRole("region", { name: "模型与推理强度 Token 明细" })).toBeVisible();
  await expect(page.getByText("上游邮箱", { exact: true })).toBeVisible();
  await expect(page.getByText("Token 总计", { exact: true })).toBeVisible();
  await expect(page.getByText("gpt-5.6-sol", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "重启容器" })).toBeVisible();
  await expect(page.getByRole("button", { name: "迁移全部用户" })).toBeVisible();

  await screenshotExpect(page).toHaveScreenshot("react-accounts-expanded-desktop-dark.png", { fullPage: false });
});

test("账号周额度卡片在窄屏与移动端不产生横向溢出", async ({ page }) => {
  await setTheme(page, "light");
  await login(page, "/admin/overview", "账号周额度");

  for (const viewport of [{ width: 1024, height: 768 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/admin/overview");
    const card = page.locator(".overview-account-quota");
    await expect(card).toBeVisible();
    await card.scrollIntoViewIfNeeded();
    await expect(card.getByRole("progressbar", { name: "账号平均周额度已用" })).toBeVisible();
    await expect(card.getByRole("link", { name: "查看账号详情" })).toHaveAttribute("href", "/admin/accounts");

    const geometry = await card.evaluate((element) => {
      const bounds = element.getBoundingClientRect();
      const metrics = element.querySelector<HTMLElement>(".overview-account-quota-metrics");
      const values = [...element.querySelectorAll<HTMLElement>(".overview-account-quota-metrics dd")];
      return {
        viewportWidth: window.innerWidth,
        bodyScrollWidth: document.body.scrollWidth,
        left: bounds.left,
        right: bounds.right,
        metricsClientWidth: metrics?.clientWidth ?? 0,
        metricsScrollWidth: metrics?.scrollWidth ?? 0,
        valueFontSizes: values.map((value) => Number.parseFloat(getComputedStyle(value).fontSize)),
        valuesFit: values.every((value) => value.scrollWidth <= value.clientWidth)
      };
    });
    expect(geometry.bodyScrollWidth).toBeLessThanOrEqual(geometry.viewportWidth);
    expect(geometry.left).toBeGreaterThanOrEqual(0);
    expect(geometry.right).toBeLessThanOrEqual(geometry.viewportWidth + 1);
    expect(geometry.metricsScrollWidth).toBeLessThanOrEqual(geometry.metricsClientWidth);
    expect(geometry.valueFontSizes.every((size) => size >= 16)).toBe(true);
    expect(geometry.valuesFit).toBe(true);
  }
});

test("使用中心横向 Tab 在桌面、窄屏与移动端完整利用内容宽度", async ({ page }) => {
  test.setTimeout(60_000);
  await setTheme(page, "light");
  await installUsageVisualBackend(page);

  for (const viewport of viewports) {
    await page.setViewportSize(viewport);
    await page.goto("http://127.0.0.1:5194/usage/");
    const orderedTabs = page.locator(".usage-primary-tabs [role=tab]");
    await expect(orderedTabs).toHaveCount(2);
    await expect(orderedTabs.nth(0)).toContainText("账号明细");
    await expect(orderedTabs.nth(1)).toHaveText("每日用量");
    await expect(page.getByRole("tab", { name: "账号明细" })).toHaveAttribute("aria-selected", "true");
    const resetTime = page.locator(".usage-personal-quota-detail > time");
    await expect(resetTime).toHaveText(/重置：\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}/);
    expect(await resetTime.evaluate((time) => {
      const timeRect = time.getBoundingClientRect();
      const detailRect = time.parentElement?.getBoundingClientRect();
      return Boolean(detailRect && timeRect.left >= detailRect.left && timeRect.right <= detailRect.right + 1
        && time.scrollWidth <= time.clientWidth);
    })).toBe(true);

    for (const section of ["trend", "accounts"] as const) {
      await page.getByRole("tab", { name: section === "trend" ? "每日用量" : "账号明细" }).click();
      const surface = page.locator(".usage-detail-sections");
      await expect(surface).toBeVisible();
      const geometry = await page.evaluate(() => {
        const topCard = document.querySelector<HTMLElement>(".usage-key-card");
        const content = document.querySelector<HTMLElement>(".usage-detail-sections");
        const tabs = [...document.querySelectorAll<HTMLElement>(".usage-primary-tabs [role=tab]")];
        const topCardRect = topCard?.getBoundingClientRect();
        const contentRect = content?.getBoundingClientRect();
        return {
          topCardLeft: topCardRect?.left ?? 0,
          topCardRight: topCardRect?.right ?? 0,
          contentLeft: contentRect?.left ?? 0,
          contentRight: contentRect?.right ?? 0,
          viewportWidth: window.innerWidth,
          bodyScrollWidth: document.body.scrollWidth,
          tabs: tabs.map((tab) => {
            const rect = tab.getBoundingClientRect();
            return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
          })
        };
      });

      expect(geometry.tabs).toHaveLength(2);
      expect(Math.abs(geometry.topCardLeft - geometry.contentLeft)).toBeLessThanOrEqual(1);
      expect(Math.abs(geometry.topCardRight - geometry.contentRight)).toBeLessThanOrEqual(1);
      expect(Math.abs(geometry.tabs[0].top - geometry.tabs[1].top)).toBeLessThanOrEqual(1);
      expect(Math.abs(geometry.tabs[0].bottom - geometry.tabs[1].bottom)).toBeLessThanOrEqual(1);
      expect(geometry.tabs[1].left).toBeGreaterThanOrEqual(geometry.tabs[0].right);
      expect(geometry.tabs.every((tab) => tab.left >= geometry.contentLeft && tab.right <= geometry.contentRight)).toBe(true);
      expect(geometry.bodyScrollWidth).toBeLessThanOrEqual(geometry.viewportWidth);
      await expect(page.getByRole("tab", { name: section === "trend" ? "每日用量" : "账号明细" })).toHaveAttribute("aria-selected", "true");
      const sectionActions = page.locator(viewport.width <= 1120
        ? `.usage-mobile-panel-actions.${section === "trend" ? "usage-trend-toolbar-actions" : "usage-tab-toolbar-actions"}`
        : `.ant-tabs-extra-content .${section === "trend" ? "usage-trend-toolbar-actions" : "usage-tab-toolbar-actions"}`);
      await expect(sectionActions).toBeVisible();
      await expect(sectionActions.locator(".usage-updated")).toHaveText(/数据更新\s*\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}/);
      expect(await sectionActions.evaluate((toolbar) => {
        const controls = toolbar.querySelector<HTMLElement>(".usage-window-switcher, .usage-trend-windows")?.getBoundingClientRect();
        const updated = toolbar.querySelector<HTMLElement>(".usage-updated")?.getBoundingClientRect();
        return Boolean(controls && updated && (window.innerWidth <= 680
          ? updated.bottom <= controls.top
          : updated.right <= controls.left));
      })).toBe(true);
      const centeredControls = page.locator(section === "trend"
        ? ".usage-trend-dimensions button, .usage-trend-windows:visible button"
        : ".usage-window-switcher:visible button");
      expect(await centeredControls.evaluateAll((buttons) => buttons.every((button) => {
        const style = getComputedStyle(button);
        return style.display === "flex" && style.alignItems === "center" && style.justifyContent === "center" && style.textAlign === "center";
      }))).toBe(true);
      await expect(page.locator(".usage-primary-tabs .ant-tabs-nav-operations")).toBeHidden();
      await expect(page.locator(".usage-section-switcher")).toHaveCount(0);
    }
  }
});

const stateCases = [
  {
    slug: "overview",
    path: "/admin/overview",
    primary: "**/admin/api/overview/summary",
    loading: "正在加载总览",
    error: "总览数据加载失败",
    empty: "所选范围内没有账号 Token 数据",
    stateSurface: "page",
    emptyRoutes: [
      "**/admin/api/overview/summary",
      "**/admin/api/overview/status",
      "**/admin/api/overview/usage?*"
    ]
  },
  {
    slug: "accounts",
    path: "/admin/accounts",
    primary: "**/admin/api/accounts?*",
    loading: "正在加载账号数据",
    error: "视觉回归模拟错误",
    empty: "还没有 CPA 账号",
    stateSurface: "catalog"
  },
  {
    slug: "users",
    path: "/admin/users",
    primary: "**/admin/api/users?*",
    loading: "正在加载用户目录",
    error: "视觉回归模拟错误",
    empty: "还没有用户",
    stateSurface: "catalog"
  },
  {
    slug: "teams",
    path: "/admin/teams",
    primary: "**/admin/api/teams",
    loading: "正在加载团队目录",
    error: "视觉回归模拟错误",
    empty: "没有匹配的团队",
    stateSurface: "catalog"
  },
  {
    slug: "runtime",
    path: "/admin/runtime",
    primary: "**/admin/api/runtime/services",
    loading: "正在加载运行维护",
    error: "视觉回归模拟错误",
    empty: "当前没有可见容器服务",
    stateSurface: "catalog"
  },
  {
    slug: "configuration",
    path: "/admin/configuration",
    primary: "**/admin/api/settings/configuration",
    loading: "正在加载配置中心",
    error: "配置中心加载失败",
    empty: "当前没有可配置项",
    stateSurface: "page"
  }
] as const;

for (const stateCase of stateCases) {
  test(`${stateCase.slug} 加载状态`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(page, "light");
    let releaseRequest = () => {};
    const requestGate = new Promise<void>((resolve) => {
      releaseRequest = resolve;
    });
    await page.route(stateCase.primary, async (route) => {
      await requestGate;
      await route.continue();
    });
    try {
      await login(page, stateCase.path);
      if (stateCase.stateSurface === "page") {
        await expect(page.getByLabel(stateCase.loading, { exact: true })).toBeVisible();
      } else {
        await expect(page.locator(".top-bar-refresh-state")).toHaveText("正在刷新");
      }
      await screenshotExpect(page).toHaveScreenshot(`react-${stateCase.slug}-state-loading.png`, { fullPage: false });
    } finally {
      releaseRequest();
    }
  });

  test(`${stateCase.slug} 空数据状态`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(page, "light");
    const matchers = "emptyRoutes" in stateCase ? stateCase.emptyRoutes : [stateCase.primary];
    for (const matcher of matchers) {
      await page.route(matcher, async (route) => fulfillEmpty(route, stateCase.slug));
    }
    await login(page, stateCase.path, stateCase.empty);
    await screenshotExpect(page).toHaveScreenshot(`react-${stateCase.slug}-state-empty.png`, { fullPage: false });
  });

  test(`${stateCase.slug} 错误状态`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(page, "light");
    await page.route(stateCase.primary, (route) => route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "visual_test", message: "视觉回归模拟错误" } })
    }));
    await login(page, stateCase.path);
    if (stateCase.stateSurface === "page") {
      await expect(page.getByText(stateCase.error, { exact: false }).first()).toBeVisible();
    } else {
      await expect(page.locator(".top-bar-refresh-state")).toHaveText("刷新失败");
      await expect(page.getByText(stateCase.error, { exact: true }).first()).toBeVisible();
    }
    await screenshotExpect(page).toHaveScreenshot(`react-${stateCase.slug}-state-error.png`, { fullPage: false });
  });
}

test("所有菜单滚动时保持页面标题固定", async ({ page }) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await login(page, routes[0].path);

  for (const route of routes) {
    if (route.slug === "setup") continue;
    await openRoute(page, route);
    const topBar = page.locator(".top-bar");
    const before = await topBar.boundingBox();
    await page.locator(".main-surface").evaluate((element) => { element.scrollTop = 1_000; });
    await expect.poll(async () => (await topBar.boundingBox())?.y).toBe(before?.y);
    await page.locator(".main-surface").evaluate((element) => { element.scrollTop = 0; });
  }
});

test("共享表格视口保持动态高度、右侧滚动槽和正确边界阴影", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  let accountReads = 0;
  await page.route("**/admin/api/accounts?*", async (route) => {
    accountReads += 1;
    const response = await route.fetch();
    const payload = await response.json() as { accounts: Array<Record<string, unknown>> };
    const source = payload.accounts;
    if (accountReads > 1) payload.accounts = Array.from({ length: 24 }, (_, index) => {
      const account = structuredClone(source[index % source.length]);
      const id = `cpa-scroll-${String(index + 1).padStart(2, "0")}`;
      return {
        ...account,
        id,
        email: `${id}@example.com`,
        port: 19_000 + index,
        service: `cliproxy-${id}`,
        default: index === 0,
        account_state: {
          ...(account.account_state as Record<string, unknown>),
          account: id
        }
      };
    });
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/accounts", "更新通道");
  await page.addStyleTag({ content: ".admin-table-viewport::before,.admin-table-viewport::after,.native-table-viewport::before,.native-table-viewport::after{transition:none!important}" });

  const viewport = page.locator(".account-table-state .admin-table-viewport");
  const body = viewport.locator(".ant-table-body");
  await expect(viewport).toHaveAttribute("data-scroll-overflow", "false");
  await expect(body.locator(".account-summary-row").first()).toBeVisible();
  const compactColumn = await page.evaluate(() => {
    const header = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-header th:nth-child(8)");
    const token = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-body .account-summary-row td:nth-child(8)");
    if (!header || !token) throw new Error("账号表格初始列锚点缺失");
    const headerRect = header.getBoundingClientRect();
    const tokenRect = token.getBoundingClientRect();
    return {
      headerLeft: headerRect.left,
      headerRight: headerRect.right,
      bodyLeftDelta: Math.abs(headerRect.left - tokenRect.left),
      bodyRightDelta: Math.abs(headerRect.right - tokenRect.right)
    };
  });
  expect(compactColumn.bodyLeftDelta).toBeLessThanOrEqual(1);
  expect(compactColumn.bodyRightDelta).toBeLessThanOrEqual(1);

  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect.poll(() => accountReads).toBeGreaterThan(1);
  await expect.poll(() => body.evaluate((element) => element.scrollHeight - element.clientHeight)).toBeGreaterThan(100);
  await expect(viewport).toHaveAttribute("data-scroll-overflow", "true");
  await expect(viewport).toHaveClass(/can-scroll-down/);
  await expect(viewport).not.toHaveClass(/can-scroll-up/);
  expect(await body.evaluate((element) => getComputedStyle(element).scrollbarGutter)).toBe("stable");

  const topState = await viewport.evaluate((element) => ({
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }));
  expect(topState).toEqual({ before: "0", after: "1" });

  await body.evaluate((element) => {
    element.scrollTop = (element.scrollHeight - element.clientHeight) / 2;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(viewport).toHaveClass(/can-scroll-up/);
  await expect(viewport).toHaveClass(/can-scroll-down/);
  expect(await viewport.evaluate((element) => ({
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }))).toEqual({ before: "1", after: "1" });

  await body.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(viewport).toHaveClass(/can-scroll-up/);
  await expect(viewport).not.toHaveClass(/can-scroll-down/);
  expect(await viewport.evaluate((element) => ({
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }))).toEqual({ before: "1", after: "0" });

  const layout = await page.evaluate(() => {
    const main = document.querySelector<HTMLElement>(".main-surface");
    const accountPage = document.querySelector<HTMLElement>(".account-page");
    const tableViewport = document.querySelector<HTMLElement>(".account-table-state .admin-table-viewport");
    const tableBody = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-body");
    const headerCell = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-header th:nth-child(8)");
    const tokenCell = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-body .account-summary-row td:nth-child(8)");
    if (!main || !accountPage || !tableViewport || !tableBody || !headerCell || !tokenCell) throw new Error("账号表格几何锚点缺失");
    const pageRect = accountPage.getBoundingClientRect();
    const viewportRect = tableViewport.getBoundingClientRect();
    const headerRect = headerCell.getBoundingClientRect();
    const tokenRect = tokenCell.getBoundingClientRect();
    return {
      mainOverflowY: getComputedStyle(main).overflowY,
      mainHasVerticalOverflow: main.scrollHeight > main.clientHeight + 1,
      bottomGap: Math.round((pageRect.bottom - viewportRect.bottom) * 100) / 100,
      viewportHeight: viewportRect.height,
      tokenAlignment: getComputedStyle(tokenCell).textAlign,
      tokenVerticalAlignment: getComputedStyle(tokenCell).verticalAlign,
      rightScrollbarSlotWidth: tableBody.offsetWidth - tableBody.clientWidth,
      finalColumnLeftDelta: Math.abs(headerRect.left - tokenRect.left),
      finalColumnRightDelta: Math.abs(headerRect.right - tokenRect.right)
    };
  });
  expect(layout.mainOverflowY).toBe("hidden");
  expect(layout.mainHasVerticalOverflow).toBe(false);
  expect(layout.bottomGap).toBeGreaterThanOrEqual(29);
  expect(layout.bottomGap).toBeLessThanOrEqual(31);
  expect(layout.viewportHeight).toBeGreaterThan(150);
  expect(layout.tokenAlignment).toBe("right");
  expect(layout.tokenVerticalAlignment).toBe("middle");
  // Overlay scrollbars report zero occupied width; classic scrollbars report
  // their native right-side slot.  Header/body alignment below must hold in
  // either mode and is the user-visible invariant.
  expect(layout.rightScrollbarSlotWidth).toBeGreaterThanOrEqual(0);
  expect(layout.rightScrollbarSlotWidth).toBeLessThanOrEqual(16);
  expect(layout.finalColumnLeftDelta).toBeLessThanOrEqual(1);
  expect(layout.finalColumnRightDelta).toBeLessThanOrEqual(1);
  expect(Math.abs(layout.finalColumnLeftDelta - compactColumn.bodyLeftDelta)).toBeLessThanOrEqual(1);
  expect(Math.abs(layout.finalColumnRightDelta - compactColumn.bodyRightDelta)).toBeLessThanOrEqual(1);

  await page.goto("/admin/overview");
  await page.getByRole("tab", { name: "CPA 账号 Token 统计" }).click();
  const naturalTable = page.locator(".overview-legacy-table-wrap").first();
  await expect(naturalTable).toHaveAttribute("data-scroll-overflow", "false");
  await expect(naturalTable).not.toHaveClass(/can-scroll-up|can-scroll-down/);
  expect(await naturalTable.evaluate((element) => ({
    gutter: getComputedStyle(element).scrollbarGutter,
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }))).toEqual({ gutter: "auto", before: "0", after: "0" });
});

for (const viewport of [
  ...viewports,
  { name: "workspace", width: 1280, height: 900 },
  { name: "full-hd", width: 1920, height: 1080 },
  { name: "wide", width: 2560, height: 1440 }
]) {
  for (const theme of ["light", "dark"] as const) {
    test(`Token 筛选栏布局与无背景 Tab：${viewport.name} ${theme}`, async ({ page }) => {
      await page.setViewportSize(viewport);
      await setTheme(page, theme);
      await login(page, "/admin/overview", "Token 使用");
      const card = page.locator(".overview-token-monitor-card");
      await expect(card.locator(".section-kicker")).toHaveText("TOKEN MONITOR");
      await expect(card.getByText("按时间与使用主体查看趋势")).toHaveCount(0);

      const layout = await card.locator(".usage-monitor-filters").evaluate((filters) => {
        const time = filters.querySelector(".overview-token-window-row")!.getBoundingClientRect();
        const scope = filters.querySelector(".overview-token-scope-filters")!.getBoundingClientRect();
        const controls = [...filters.querySelectorAll(".overview-token-mode-control, .usage-variable, .overview-legacy-refresh-controls")];
        const bounds = filters.getBoundingClientRect();
        return {
          scopeRightOfTime: scope.left >= time.right + 12 && Math.abs(scope.top - time.top) <= 1,
          scopeBelowTime: scope.top >= time.bottom,
          overflow: filters.scrollWidth - filters.clientWidth,
          controlsInside: controls.every((control) => {
            const rect = control.getBoundingClientRect();
            return rect.left >= bounds.left && rect.right <= bounds.right;
          }),
          scopeSingleRow: controls.every((control) => Math.abs(control.getBoundingClientRect().bottom - controls[0].getBoundingClientRect().bottom) <= 1)
        };
      });
      expect(layout.overflow).toBeLessThanOrEqual(1);
      expect(layout.controlsInside).toBe(true);
      if (viewport.width > 1120) expect(layout.scopeRightOfTime).toBe(true);
      else expect(layout.scopeBelowTime).toBe(true);
      if (viewport.width >= 1440) expect(layout.scopeSingleRow).toBe(true);

      const tabs = card.getByRole("tablist", { name: "Token 使用数据视角" });
      for (const name of ["全部账号", "CPA 账号 Token 统计", "用户 Token 统计"]) {
        const tab = tabs.getByRole("tab", { name, exact: true });
        await expect(tab).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
        await expect(tab).toHaveCSS("background-image", "none");
        await tab.hover();
        await expect(tab).toHaveCSS("background-image", "none");
        await tab.click();
        await expect(tab).toHaveAttribute("aria-selected", "true");
        await expect(tab).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
        await expect(tab).toHaveCSS("background-image", "none");
        await expect.poll(() => tab.evaluate((element) => getComputedStyle(element, "::after").opacity)).toBe("1");
        await expect(card.getByRole("tabpanel", { name, exact: true })).toBeVisible();
        const chartCanvas = card.getByRole("tabpanel", { name, exact: true }).locator(".usage-chart-canvas");
        await expect(chartCanvas).toHaveCSS("height", "500px");
        await expect(chartCanvas.locator("svg")).toHaveAttribute("height", "500");
        const aggregateSummary = card.getByRole("region", { name: "全部账号统计摘要" });
        if (name === "全部账号") {
          await expect(aggregateSummary).toBeVisible();
          await expect(aggregateSummary.getByRole("columnheader")).toHaveCount(7);
          await expect(aggregateSummary.getByRole("cell")).toHaveCount(7);
          for (const cell of await aggregateSummary.getByRole("cell").all()) {
            await expect(cell).toHaveCSS("vertical-align", "middle");
          }
          await expect(aggregateSummary.locator(".overview-chart-summary-time time")).toBeVisible();
          await expect(aggregateSummary.locator(".overview-chart-summary-mode .overview-chart-mode-tag")).toBeVisible();
          for (const token of await aggregateSummary.locator(".overview-chart-summary-token").all()) {
            await expect(token).toHaveCSS("text-align", "right");
            await expect(token.locator("strong")).toHaveText(/[\d,.]+ (Token|K|M|B)/);
            await expect(token.locator("small")).toHaveText(/^[\d,]+ Token$/);
          }
          expect(await aggregateSummary.evaluate((element) => {
            const chart = element.previousElementSibling!.getBoundingClientRect();
            const rect = element.getBoundingClientRect();
            const panel = element.parentElement!.getBoundingClientRect();
            return rect.top >= chart.bottom && rect.right <= panel.right + 1;
          })).toBe(true);
        } else {
          await expect(aggregateSummary).toHaveCount(0);
        }
      }
      const firstTab = tabs.getByRole("tab", { name: "全部账号", exact: true });
      await firstTab.focus();
      await firstTab.press("Enter");
      await expect(firstTab).toHaveAttribute("aria-selected", "true");
      await expect(firstTab).toBeFocused();
      await expect(firstTab).toHaveCSS("background-image", "none");
      await expect(card.getByRole("tabpanel", { name: "全部账号", exact: true })).toBeVisible();
    });
  }
}

test("全部账号图下摘要跟随口径、时段及时间范围", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await page.route("**/admin/api/overview/usage?*", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    const sixHours = new URL(route.request().url()).searchParams.get("window") === "21600";
    const start = Date.parse(sixHours ? "2026-09-05T06:00:00+08:00" : "2026-09-05T08:00:00+08:00") / 1000;
    payload.buckets = [start, start + 900];
    payload.accounts = [{
      ...payload.accounts[0],
      values: sixHours ? [50, 150] : [100, 300],
      weighted_values: sixHours ? [100, 300] : [200, 600]
    }];
    payload.window_timezone = "Asia/Shanghai";
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/overview", "Token 使用");
  const card = page.locator(".overview-token-monitor-card");
  const summary = card.getByRole("region", { name: "全部账号统计摘要" });
  const point = summary.locator('[data-metric="point"]');
  const pointHeader = summary.locator('[data-metric-header="point"]');
  const time = summary.locator(".overview-chart-summary-time time");
  const chart = card.locator(".overview-legacy-chart");
  const metric = (name: string) => {
    const key = ({ 当前值: "current", 范围内总量: "total", 平均值: "average", 最大值: "maximum" } as Record<string, string>)[name];
    return summary.locator(`[data-metric="${key}"] strong`);
  };
  await expect(metric("当前值")).toHaveText("300 Token");
  await expect(metric("范围内总量")).toHaveText("400 Token");
  await expect(metric("平均值")).toHaveText("200 Token");
  await expect(metric("最大值")).toHaveText("300 Token");
  await expect(pointHeader).toHaveText("最新时段");
  await expect(time).toHaveText("2026/09/05 08:15:00");
  await expect(chart.locator("svg text").filter({ hasText: "峰值 300 Token" })).toBeVisible();
  await chart.hover({ position: { x: 80, y: 100 } });
  await expect(pointHeader).toHaveText("所选时段");
  await expect(point.locator("strong")).toHaveText("100 Token");
  await card.getByRole("heading", { name: "Token 使用" }).hover();
  await expect(pointHeader).toHaveText("最新时段");
  await expect(point.locator("strong")).toHaveText("300 Token");
  await chart.focus();
  await chart.press("Home");
  await expect(pointHeader).toHaveText("所选时段");
  await expect(point.locator("strong")).toHaveText("100 Token");
  await expect(time).toHaveText("2026/09/05 08:00:00");
  await expect(metric("当前值")).toHaveText("300 Token");
  await expect(metric("范围内总量")).toHaveText("400 Token");
  await expect(card.locator(".overview-chart-tooltip")).toHaveCount(0);
  await chart.press("Escape");
  await expect(pointHeader).toHaveText("最新时段");
  await expect(point.locator("strong")).toHaveText("300 Token");
  await chart.press("ArrowLeft");
  await expect(point.locator("strong")).toHaveText("100 Token");
  await chart.press("ArrowRight");
  await expect(point.locator("strong")).toHaveText("300 Token");
  await card.getByRole("button", { name: "加权", exact: true }).click();
  await expect(metric("范围内总量")).toHaveText("800 Token");
  await expect(metric("当前值")).toHaveText("600 Token");
  await expect(metric("平均值")).toHaveText("400 Token");
  await expect(metric("最大值")).toHaveText("600 Token");
  await expect(chart.locator("svg text").filter({ hasText: "峰值 600 Token" })).toBeVisible();
  await expect(chart.locator("svg text").filter({ hasText: "峰值 300 Token" })).toHaveCount(0);
  await expect(summary.locator(".overview-chart-summary-token small")).toHaveText(["600 Token", "600 Token", "800 Token", "400 Token", "600 Token"]);
  await expect(summary.locator(".overview-chart-mode-tag.weighted")).toHaveText("加权");
  await chart.focus();
  await chart.press("Home");
  await expect(point.locator("strong")).toHaveText("200 Token");
  await expect(point.locator("small")).toHaveText("200 Token");
  await card.getByRole("button", { name: "6 小时", exact: true }).click();
  await expect(metric("范围内总量")).toHaveText("400 Token");
  await expect(time).toHaveText("2026/09/05 06:15:00");
  await expect(point.locator("strong")).toHaveText("300 Token");
  await expect(summary.locator(".overview-chart-summary-token small")).toHaveText(["300 Token", "300 Token", "400 Token", "200 Token", "300 Token"]);
  await summary.screenshot({ path: test.info().outputPath("token-summary.png") });
});

test("管理中心 Token 卡片分层展示实际范围、趋势和可滚动明细", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await page.route("**/admin/api/overview/usage?*", async (route) => {
    const response = await route.fetch();
    const payload = await response.json() as { accounts: Array<Record<string, unknown>> };
    const source = payload.accounts;
    payload.accounts = Array.from({ length: 18 }, (_, index) => ({
      ...structuredClone(source[index % source.length]),
      name: `cpa-token-${String(index + 1).padStart(2, "0")}`
    }));
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/overview", "Token 使用");

  const card = page.locator(".overview-token-monitor-card");
  const tabHeader = card.locator(".overview-token-workspace-header");
  const tabs = tabHeader.getByRole("tab");
  const summary = card.getByLabel("全部账号统计摘要");
  const dataScroll = card.locator(".overview-token-data-scroll");
  await expect(card.getByText("实际统计范围")).toHaveCount(0);
  await expect(card.locator(".overview-token-window-value")).toHaveCount(2);
  await expect(card.locator(".overview-token-window-value").first()).toContainText(/起始时间\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}/);
  await expect(card.locator(".overview-token-window-value").last()).toContainText(/结束时间\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}/);
  await expect(card.locator(".overview-collector-state")).toHaveText("采集正常");
  await expect(card.locator(".overview-collector-meta time")).toHaveText(/\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}/);
  const filterGeometry = await card.evaluate((element) => {
    const timeControls = element.querySelector<HTMLElement>(".overview-legacy-window-segments");
    const startBoundary = element.querySelector<HTMLElement>(".overview-token-window-value:first-child strong");
    const endBoundary = element.querySelector<HTMLElement>(".overview-token-window-value:last-child strong");
    const headingRow = element.querySelector<HTMLElement>(".overview-token-heading-row");
    const filters = element.querySelector<HTMLElement>(".usage-monitor-filters");
    const refreshSelect = element.querySelector<HTMLElement>(".usage-refresh-control .enhanced-select-trigger");
    const refreshButton = element.querySelector<HTMLElement>(".overview-legacy-refresh-button");
    const collectorMeta = element.querySelector<HTMLElement>(".overview-collector-meta");
    const scopeFilters = element.querySelector<HTMLElement>(".overview-token-scope-filters");
    if (!timeControls || !startBoundary || !endBoundary || !headingRow || !filters || !refreshSelect || !refreshButton || !collectorMeta || !scopeFilters) {
      throw new Error("Token 筛选栏几何锚点缺失");
    }
    const timeRect = timeControls.getBoundingClientRect();
    const startRect = startBoundary.getBoundingClientRect();
    const endRect = endBoundary.getBoundingClientRect();
    const selectRect = refreshSelect.getBoundingClientRect();
    const buttonRect = refreshButton.getBoundingClientRect();
    const collectorRect = collectorMeta.getBoundingClientRect();
    const scopeRect = scopeFilters.getBoundingClientRect();
    const headingRect = headingRow.getBoundingClientRect();
    const filtersRect = filters.getBoundingClientRect();
    return {
      timeHeight: timeRect.height,
      startHeight: startRect.height,
      endHeight: endRect.height,
      selectHeight: selectRect.height,
      buttonHeight: buttonRect.height,
      controlsAligned: Math.abs(selectRect.top - buttonRect.top),
      statusInHeading: collectorRect.top >= headingRect.top - 1 && collectorRect.bottom <= headingRect.bottom + 1,
      headingAboveFilters: headingRect.bottom < filtersRect.top,
      statusOnFirstRow: collectorRect.bottom <= scopeRect.top,
      refreshWithinScope: selectRect.top >= scopeRect.top - 1 && buttonRect.bottom <= scopeRect.bottom + 1,
      scopeRightOfTime: scopeRect.left >= timeControls.closest("fieldset")!.getBoundingClientRect().right + 12
    };
  });
  expect(Math.abs(filterGeometry.timeHeight - filterGeometry.startHeight)).toBeLessThanOrEqual(1);
  expect(Math.abs(filterGeometry.startHeight - filterGeometry.endHeight)).toBeLessThanOrEqual(1);
  expect(Math.abs(filterGeometry.timeHeight - filterGeometry.selectHeight)).toBeLessThanOrEqual(1);
  expect(Math.abs(filterGeometry.selectHeight - filterGeometry.buttonHeight)).toBeLessThanOrEqual(1);
  expect(filterGeometry.controlsAligned).toBeLessThanOrEqual(1);
  expect(filterGeometry.statusInHeading).toBe(true);
  expect(filterGeometry.headingAboveFilters).toBe(true);
  expect(filterGeometry.statusOnFirstRow).toBe(true);
  expect(filterGeometry.refreshWithinScope).toBe(true);
  expect(filterGeometry.scopeRightOfTime).toBe(true);
  await expect(tabs).toHaveCount(3);
  await expect(summary).toBeVisible();
  await expect(dataScroll.locator(".overview-legacy-chart")).toBeVisible();
  await expect(dataScroll.locator(".overview-legacy-chart")).toHaveCSS("min-height", "500px");
  await expect(dataScroll.locator(".overview-token-series-legend")).toHaveCount(0);
  await expect(dataScroll.getByLabel("CPA用量明细表格")).toHaveCount(0);
  const aggregateChart = dataScroll.locator(".overview-legacy-chart");
  await aggregateChart.hover({ position: { x: 540, y: 150 } });
  await expect(page.locator(".overview-chart-tooltip")).toHaveCount(0);
  await expect(summary).toContainText("所选时段");
  await expect(summary.locator(".overview-chart-mode-tag.unweighted")).toHaveText("未加权");
  await expect(summary).toContainText("当前值");
  await expect(summary).toContainText("范围内总量");
  await expect(summary).toContainText("平均值");
  await expect(summary).toContainText("最大值");

  await card.getByRole("tab", { name: "CPA 账号 Token 统计" }).click();
  await expect(summary).toHaveCount(0);
  const detailTable = dataScroll.getByLabel("CPA用量明细表格");
  await expect(detailTable).toBeVisible();
  await expect(detailTable.locator(".usage-monitor-help")).toHaveCount(0);
  await expect(detailTable.locator("tbody tr")).toHaveCount(10);

  const geometry = await card.evaluate((element) => {
    const header = element.querySelector<HTMLElement>(".overview-token-workspace-header");
    const scroller = element.querySelector<HTMLElement>(".overview-token-data-scroll");
    const chart = element.querySelector<HTMLElement>(".overview-legacy-chart");
    const table = element.querySelector<HTMLElement>(".overview-token-detail-table");
    const tabButtons = [...element.querySelectorAll<HTMLElement>(".overview-token-view-switch [role=tab]")];
    if (!header || !scroller || !chart || !table || tabButtons.length !== 3) {
      throw new Error("Token 卡片几何锚点缺失");
    }
    const headerRect = header.getBoundingClientRect();
    const scrollRect = scroller.getBoundingClientRect();
    const chartRect = chart.getBoundingClientRect();
    const tableRect = table.getBoundingClientRect();
    return {
      tabsAboveData: headerRect.bottom <= scrollRect.top + 1,
      chartAboveTable: chartRect.bottom <= tableRect.top + 50,
      tabWidths: tabButtons.map((button) => button.getBoundingClientRect().width),
      workspaceOverflowY: getComputedStyle(scroller).overflowY,
      detailOverflowY: getComputedStyle(table).overflowY,
      tenRowHeight: table.querySelector("thead")!.getBoundingClientRect().height + 10 * table.querySelector("tbody tr")!.getBoundingClientRect().height,
      detailClientHeight: table.clientHeight,
      detailScrollHeight: table.scrollHeight
    };
  });
  expect(geometry.tabsAboveData).toBe(true);
  expect(geometry.chartAboveTable).toBe(true);
  expect(Math.max(...geometry.tabWidths) - Math.min(...geometry.tabWidths)).toBeLessThanOrEqual(1);
  expect(geometry.workspaceOverflowY).toBe("visible");
  expect(geometry.detailOverflowY).toBe("auto");
  expect(Math.abs(geometry.detailClientHeight - geometry.tenRowHeight)).toBeLessThanOrEqual(1);
  expect(geometry.detailScrollHeight).toBeGreaterThan(geometry.detailClientHeight);

  await detailTable.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(detailTable.locator("tbody tr")).toHaveCount(18);

  await expect(card.getByLabel("CPA 账号统计摘要")).toHaveCount(0);
  await card.getByRole("tab", { name: "用户 Token 统计" }).click();
  await expect(card.getByLabel("用户统计摘要")).toHaveCount(0);
  await expect(card.getByRole("tabpanel", { name: "用户 Token 统计" }).getByLabel("用户用量明细表格")).toBeVisible();
});

test("管理中心左下角保留当前版本和更新心跳，悬停显示版本对比且点击不跳转", async ({ page }) => {
  let releasePayload = {
    configured: true,
    current_version: "v2.0.0-rc.50",
    latest_version: "v2.0.0",
    available: true,
    checked_at: 1_787_500_800,
    status: "ok"
  };
  await page.route("**/admin/api/release*", async (route) => {
    await fulfillJSON(route, releasePayload);
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await login(page, "/admin/overview", "Token 使用");

  await expect(page.locator(".release-notice")).toHaveCount(0);
  const desktopEntry = page.locator(".side-nav-footer .release-version-indicator");
  await expect(desktopEntry).toBeVisible();
  await expect(desktopEntry).toHaveText("v2.0.0-rc.50");
  const versionOverflow = await desktopEntry.locator(".release-version-number").evaluate((element) => element.scrollWidth - element.clientWidth);
  expect(versionOverflow).toBeLessThanOrEqual(1);
  await expect(desktopEntry.locator(".release-version-heartbeat")).toBeVisible();
  await expect(desktopEntry).toHaveAttribute("data-update", "true");
  const footerOrder = await page.locator(".side-nav-footer").evaluate((footer) => {
    const release = footer.querySelector<HTMLElement>(".release-version-indicator");
    const auth = footer.querySelector<HTMLElement>(".side-nav-auth-status");
    const releaseBounds = release!.getBoundingClientRect();
    const authBounds = auth!.getBoundingClientRect();
    return {
      releaseLeft: releaseBounds.left,
      authRight: authBounds.right,
      releaseCenter: releaseBounds.top + releaseBounds.height / 2,
      authCenter: authBounds.top + authBounds.height / 2
    };
  });
  expect(footerOrder.releaseLeft).toBeGreaterThan(footerOrder.authRight);
  expect(Math.abs(footerOrder.releaseCenter - footerOrder.authCenter)).toBeLessThan(1);
  await expect(desktopEntry).not.toHaveAttribute("href");
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await desktopEntry.hover();
  const versionTooltip = page.getByRole("tooltip");
  await expect(versionTooltip).toBeVisible();
  await expect(versionTooltip.locator(".release-version-comparison > span")).toHaveText(["当前版本", "最新版本"]);
  await expect(versionTooltip.locator(".release-version-comparison > strong")).toHaveText(["v2.0.0-rc.50", "v2.0.0"]);
  await test.info().attach("版本更新悬浮框", {
    body: await page.screenshot({ clip: { x: 0, y: 720, width: 300, height: 180 }, animations: "disabled" }),
    contentType: "image/png"
  });
  const originalURL = page.url();
  const originalPages = page.context().pages().length;
  await desktopEntry.click();
  await expect(page).toHaveURL(originalURL);
  expect(page.context().pages()).toHaveLength(originalPages);
  await expect(page.getByRole("region", { name: "应用版本详情" })).toHaveCount(0);
  await page.mouse.move(300, 100);
  await desktopEntry.evaluate((element) => (element as HTMLElement).blur());
  await expect(versionTooltip).not.toBeVisible();

  releasePayload = { ...releasePayload, current_version: "v1.0.0", latest_version: "v1.0.0", available: false };
  await page.reload();
  await expect(desktopEntry).toHaveText("v1.0.0");

  releasePayload = { ...releasePayload, latest_version: "v1.1.0", available: true, status: "unavailable" };
  await page.reload();
  await expect(desktopEntry).toHaveText("v1.0.0");

  releasePayload = { ...releasePayload, current_version: "v2.0.0-rc.1", latest_version: "v1.1.0", available: false, status: "ok" };
  await page.reload();
  await expect(desktopEntry).toHaveText("v2.0.0-rc.1");

  releasePayload = { ...releasePayload, current_version: "v1.0.0", available: true, status: "ok" };
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.locator(".side-nav-footer")).toBeVisible();
  await expect(page.locator(".release-version-indicator")).toHaveCount(1);
  await expect(page.locator(".top-bar .release-version-indicator")).toHaveCount(0);
  await expect(page.getByRole("region", { name: "应用版本详情" })).toHaveCount(0);
  await expect(page.locator(".main-surface")).not.toHaveCSS("overflow-x", "scroll");

  releasePayload = { ...releasePayload, latest_version: "v1.0.0", available: false };
  await page.reload();
  await expect(desktopEntry).toHaveText("v1.0.0");
  const footerBounds = await page.locator(".side-nav-footer").boundingBox();
  const mainBounds = await page.locator(".main-surface").boundingBox();
  expect(footerBounds!.x).toBe(0);
  expect(footerBounds!.y + footerBounds!.height).toBe(844);
  expect(mainBounds!.y + mainBounds!.height).toBeLessThanOrEqual(footerBounds!.y);
});

test("30 天图表 Tooltip 为单列 Top 10 且无滚动条", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await page.route("**/admin/api/overview/usage?*", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.users = Array.from({ length: 12 }, (_, index) => {
      const value = (index + 1) * 100_000;
      return {
        name: `user-${String(index + 1).padStart(2, "0")}@example.com`,
        values: payload.buckets.map(() => value),
        current: value,
        average: value,
        maximum: value,
        total: value * payload.buckets.length
      };
    });
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/overview", "Token 使用");

  const responsePromise = page.waitForResponse((response) => response.url().includes("window=2592000"));
  await page.getByRole("button", { name: "30 天", exact: true }).click();
  const response = await responsePromise;
  await response.finished();
  // Measure the complete request through Vite and the deterministic mock proxy;
  // Playwright's button actionability waits are not part of network latency.
  const elapsed = response.request().timing().responseEnd;
  expect(elapsed).toBeGreaterThanOrEqual(0);
  expect(elapsed).toBeLessThan(1_000);

  await page.getByRole("tab", { name: "用户 Token 统计" }).click();
  const chart = page.locator(".overview-legacy-chart").first();
  await expect(chart).toBeVisible();
  await chart.scrollIntoViewIfNeeded();
  await expect(chart.locator("svg")).toBeVisible();
  const box = await chart.boundingBox();
  if (!box) throw new Error("用户趋势图没有可交互区域");
  await page.mouse.move(box.x + box.width * 0.55, box.y + box.height * 0.45);

  const tooltip = page.locator(".overview-chart-tooltip[data-active=true]");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toHaveAttribute("data-layout", "single-column");
  await expect(tooltip).toContainText("未加权");
  const rows = tooltip.locator(":scope > span");
  await expect(rows).toHaveCount(10);
  await expect(rows.first().locator("b")).toHaveText("user-12@example.com");
  await expect(rows.last().locator("b")).toHaveText("user-03@example.com");
  expect(await tooltip.evaluate((element) => ({
    vertical: element.scrollHeight <= element.clientHeight,
    horizontal: element.scrollWidth <= element.clientWidth
  }))).toEqual({ vertical: true, horizontal: true });
  await screenshotExpect(page).toHaveScreenshot("react-overview-tooltip-top10.png", {
    fullPage: false,
    // Tooltip glyph antialiasing differs across supported macOS releases.
    // DOM shape, row count and overflow are asserted immediately above.
    maxDiffPixelRatio: 0.02
  });
});

test("使用中心打开管理框即读取 API Key，关闭立即清除，刷新后只展示新 Key", async ({ page }) => {
  const oldKey = "old-e2e-api-key-1234";
  const newKey = "new-e2e-api-key-9876";
  let revealRequests = 0;
  let rotateRequests = 0;
  await page.route(/\/usage\/(?:session|me)(?:\/|\?|$)/, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    if (request.method() === "GET" && path === "/usage/session") {
      await fulfillJSON(route, {
        authenticated: true,
        user: "alice@example.com",
        expires_at: 1_787_544_000,
        password_change_required: false
      });
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/profile") {
      await fulfillJSON(route, {
        user: "alice@example.com",
        current_group: "alpha",
        generated_at: 1_787_500_800
      });
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/route") {
      await fulfillJSON(route, { current_group: "alpha", generated_at: 1_787_500_800 });
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/quota") {
      await fulfillJSON(route, usageQuotaFixture());
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/accounts") {
      await fulfillJSON(route, usageAccountsFixture());
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/key") {
      revealRequests += 1;
      await fulfillJSON(route, { api_key: oldKey, generated_at: 1_787_500_800 }, { "Cache-Control": "no-store" });
      return;
    }
    if (request.method() === "POST" && path === "/usage/me/key/rotate") {
      rotateRequests += 1;
      expect(request.postDataJSON()).toEqual({ confirm: true });
      await fulfillJSON(route, {
        message: "API Key 已刷新",
        api_key: newKey,
        snapshot_generation: "generation-2"
      }, { "Cache-Control": "no-store" });
      return;
    }
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "e2e_not_found", message: path } })
    });
  });

  await page.goto("http://127.0.0.1:5194/usage/");
  await expect(page.getByRole("button", { name: "管理 API Key" })).toBeVisible();
  expect(revealRequests).toBe(0);
  await page.getByRole("button", { name: "管理 API Key" }).click();
  const keyDialog = page.getByRole("dialog", { name: "管理 API Key" });
  await expect(keyDialog).toBeVisible();
  const keyInput = page.getByLabel("API Key", { exact: true });
  await expect(keyInput).toHaveValue(oldKey);
  await expect(keyDialog.getByRole("button", { name: "查看 API Key" })).toHaveCount(0);
  expect(revealRequests).toBe(1);
  await keyDialog.locator(".ant-modal-footer").getByRole("button", { name: "关闭" }).click();
  await expect(keyInput).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(oldKey);

  await page.getByRole("button", { name: "管理 API Key" }).click();
  await expect(keyInput).toHaveValue(oldKey);
  expect(revealRequests).toBe(2);
  await page.getByRole("dialog", { name: "管理 API Key" }).getByRole("button", { name: "刷新 API Key", exact: true }).click();
  await page.getByRole("button", { name: "确认刷新并使旧 Key 失效" }).click();
  await expect(keyInput).toHaveValue(newKey);
  expect(rotateRequests).toBe(1);
  await expect(page.locator("body")).not.toContainText(oldKey);
  expect(await page.evaluate(([oldValue, newValue]) => {
    const storage = [window.localStorage, window.sessionStorage];
    return storage.some((entry) => Object.values(entry).some((value) => value === oldValue || value === newValue));
  }, [oldKey, newKey])).toBe(false);

  await keyDialog.locator(".ant-modal-footer").getByRole("button", { name: "关闭" }).click();
  await expect(keyInput).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(newKey);
});

test("使用中心客户端配置弹框直接展示必要内容", async ({ page, context }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: "http://127.0.0.1:5194" });
  await setTheme(page, "dark");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await page.getByRole("button", { name: "配置 Codex" }).click();
  const codexDialog = page.getByRole("dialog", { name: "配置 Codex" });
  await expect(codexDialog).toBeVisible();
  await expect(codexDialog.getByText("选择要完成的 Codex 任务")).toHaveCount(0);
  await expect(codexDialog.getByText("Codex 配置内容")).toBeVisible();
  await expect(codexDialog.getByText("迁移旧会话")).toBeVisible();
  await expect(codexDialog.getByRole("button", { name: "复制配置" })).toBeVisible();
  await expect(codexDialog.getByRole("button", { name: "复制迁移指令" })).toBeVisible();
  await codexDialog.locator(".portal-config-actions").getByRole("button", { name: "关闭" }).click();

  await page.getByRole("button", { name: "导入 CC Switch" }).click();
  const switchDialog = page.getByRole("dialog", { name: "完成 CC Switch 配置" });
  await expect(switchDialog).toBeVisible();
  await expect(switchDialog.getByText("Codex 配置内容")).toBeVisible();
  await expect(switchDialog.getByText("迁移旧会话")).toBeVisible();
  await expect(switchDialog.getByRole("button", { name: "复制迁移指令" })).toBeVisible();
  await expect(switchDialog.getByText("操作文件")).toHaveCount(0);
  await expect(switchDialog.getByRole("button", { name: "仅复制图片配置" })).toHaveCount(0);
  await expect(switchDialog.locator(".portal-config-actions").getByRole("button")).toHaveCount(2);
  await expect(switchDialog.locator(".portal-config-actions").getByRole("button", { name: "关闭" })).toBeVisible();
  const copyAndImport = switchDialog.locator(".portal-config-actions").getByRole("button", { name: "复制并导入" });
  await expect(copyAndImport).toBeVisible();
  await screenshotExpect(switchDialog).toHaveScreenshot("react-usage-ccswitch-config-dialog-dark.png");
  const expectedConfig = await switchDialog.locator(".portal-config-preview").first().innerText();
  await page.evaluate(() => navigator.clipboard.writeText("CPA_CLIPBOARD_SENTINEL"));
  await copyAndImport.click();
  await expect(switchDialog.getByRole("status")).toHaveText("完整配置已复制，正在打开 CC Switch…");
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(expectedConfig);
});

test("修改个人密码弹框统一边框、标签和输入框几何", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await page.getByRole("button", { name: /^用户菜单：/ }).click();
  await page.getByRole("menuitem", { name: "修改密码", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "修改个人密码" });
  await expect(dialog).toBeVisible();
  const fields = dialog.locator(".portal-password-form .ant-input-affix-wrapper");
  const labels = dialog.locator(".portal-password-form .ant-form-item-label");
  await expect(fields).toHaveCount(3);
  await expect(labels).toHaveCount(3);

  const desktopGeometry = await dialog.evaluate((element) => {
    const boxes = [...element.querySelectorAll<HTMLElement>(".portal-password-form .ant-input-affix-wrapper")]
      .map((field) => field.getBoundingClientRect());
    const labelElements = [...element.querySelectorAll<HTMLElement>(".portal-password-form .ant-form-item-label")];
    const labelBoxes = labelElements.map((label) => label.getBoundingClientRect());
    const modal = element.querySelector<HTMLElement>(".ant-modal-content, .ant-modal-container");
    const modalStyle = modal ? getComputedStyle(modal) : null;
    return {
      fieldLefts: boxes.map((box) => box.left),
      fieldWidths: boxes.map((box) => box.width),
      fieldHeights: boxes.map((box) => box.height),
      labelRights: labelBoxes.map((box) => box.right),
      labelAlignments: labelElements.map((label) => getComputedStyle(label).textAlign),
      modalBorderWidth: modalStyle?.borderTopWidth,
      modalBorderStyle: modalStyle?.borderTopStyle
    };
  });
  expect(Math.max(...desktopGeometry.fieldLefts) - Math.min(...desktopGeometry.fieldLefts)).toBeLessThanOrEqual(0.5);
  expect(Math.max(...desktopGeometry.fieldWidths) - Math.min(...desktopGeometry.fieldWidths)).toBeLessThanOrEqual(0.5);
  expect(Math.max(...desktopGeometry.fieldHeights) - Math.min(...desktopGeometry.fieldHeights)).toBeLessThanOrEqual(0.5);
  expect(Math.max(...desktopGeometry.labelRights) - Math.min(...desktopGeometry.labelRights)).toBeLessThanOrEqual(0.5);
  expect(desktopGeometry.labelAlignments).toEqual(["right", "right", "right"]);
  expect(desktopGeometry.modalBorderWidth).toBe("1px");
  expect(desktopGeometry.modalBorderStyle).toBe("solid");
  await screenshotExpect(dialog).toHaveScreenshot("react-usage-password-dialog-desktop-dark.png", {
    threshold: 0.3,
    maxDiffPixelRatio: 0.02
  });

  await page.setViewportSize({ width: 390, height: 844 });
  const mobileGeometry = await dialog.evaluate((element) => {
    const boxes = [...element.querySelectorAll<HTMLElement>(".portal-password-form .ant-input-affix-wrapper")]
      .map((field) => field.getBoundingClientRect());
    const labelElements = [...element.querySelectorAll<HTMLElement>(".portal-password-form .ant-form-item-label")];
    return {
      fieldLefts: boxes.map((box) => box.left),
      fieldWidths: boxes.map((box) => box.width),
      labelAlignments: labelElements.map((label) => getComputedStyle(label).textAlign)
    };
  });
  expect(Math.max(...mobileGeometry.fieldLefts) - Math.min(...mobileGeometry.fieldLefts)).toBeLessThanOrEqual(0.5);
  expect(Math.max(...mobileGeometry.fieldWidths) - Math.min(...mobileGeometry.fieldWidths)).toBeLessThanOrEqual(0.5);
  expect(mobileGeometry.labelAlignments).toEqual(["start", "start", "start"]);
  await screenshotExpect(dialog).toHaveScreenshot("react-usage-password-dialog-mobile-dark.png", {
    threshold: 0.3,
    maxDiffPixelRatio: 0.02
  });
});

async function setTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("cpa-ui-theme", value), theme);
}

async function login(page: Page, path: string, ready?: string) {
  await page.goto(path);
  const password = page.locator('input[type="password"]');
  await expect(password).toBeVisible();
  await password.fill("visual-preview");
  await page.getByRole("button", { name: "验证并进入" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  if (ready) await expect(page.getByText(ready, { exact: false }).first()).toBeVisible();
}

async function openRoute(page: Page, route: (typeof routes)[number]) {
  await page.goto(route.path);
  if (route.slug === "setup") {
    await expect(page.getByRole("heading", { name: "完成基础配置", level: 2 })).toBeVisible();
    await expect(page.locator(".app-shell, .side-nav, .top-bar")).toHaveCount(0);
  } else {
    await expect(page.getByRole("heading", { name: route.title, level: 1 })).toBeVisible();
  }
  await expect(page.getByText(route.ready, { exact: false }).first()).toBeVisible();
  await page.waitForTimeout(200);
}

async function expectActiveNavigationVisible(page: Page) {
  const geometry = await page.locator('.admin-nav [aria-current="page"]').evaluate((element) => {
    const item = element.getBoundingClientRect();
    const navigation = element.parentElement?.getBoundingClientRect();
    return {
      itemWidth: item.width,
      visible: Boolean(
        navigation
        && item.left >= Math.max(0, navigation.left)
        && item.right <= Math.min(window.innerWidth, navigation.right)
      )
    };
  });
  expect(geometry.itemWidth).toBeGreaterThan(50);
  expect(geometry.visible).toBe(true);
}

async function fulfillEmpty(route: Route, slug: string) {
  const response = await route.fetch();
  const payload = await response.json();
  if (slug === "overview") {
    if (route.request().url().includes("/summary")) {
      payload.summary = Object.fromEntries(Object.keys(payload.summary).map((key) => [key, 0]));
    } else if (route.request().url().includes("/status")) {
      payload.authorized_accounts = 0;
      payload.running_services = 0;
      payload.total_services = 0;
      payload.requests_5m = 0;
      payload.account_quota = {
        available: false,
        enabled_accounts: 0,
        known_accounts: 0,
        unknown_accounts: 0,
        average_used_percent: null,
        average_remaining_percent: null,
        equivalent_remaining_accounts: 0,
        exhausted_accounts: 0,
        high_risk_accounts: 0
      };
    } else {
      payload.accounts = [];
      payload.users = [];
      payload.selected_accounts = [];
      payload.selected_users = [];
    }
  } else if (slug === "accounts") {
    payload.accounts = [];
    payload.warnings = [];
  } else if (slug === "users") {
    payload.users = [];
    payload.pagination = { ...payload.pagination, page: 1, total: 0 };
  } else if (slug === "teams") {
    payload.teams = [];
  } else if (slug === "runtime") {
    payload.services = [];
  } else if (slug === "configuration") {
    payload.groups = [];
    payload.field_count = 0;
  }
  await route.fulfill({ response, json: payload });
}

// Opt-in documentation capture uses only the isolated, synthetic preview backend.
test("文档深色截图", async ({ page }) => {
  test.skip(process.env.CPAP_DOC_SCREENSHOTS !== "1", "仅按需生成公开文档截图");
  await page.setViewportSize({ width: 1920, height: 1080 });
  await setTheme(page, "dark");
  await page.route("**/admin/api/release*", (route) => fulfillJSON(route, {
    configured: true, status: "ok", current_version: "v2.0.0", latest_version: "v2.0.0", available: false, checked_at: 1787500800
  }));
  const capture = async (name: string) => {
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await page.evaluate(() => document.fonts.ready);
    await page.mouse.move(0, 0);
    await page.waitForTimeout(700);
    await page.screenshot({ path: path.resolve("../docs/assets", `screenshot-${name}.png`), animations: "disabled", caret: "hide" });
  };
  await login(page, "/admin/overview", "Token 使用");
  await capture("overview");
  await openRoute(page, routes[1]);
  await capture("accounts");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");
  await page.getByRole("tab", { name: "每日用量" }).click();
  await page.getByRole("button", { name: "模型 + 推理强度", exact: true }).click();
  await expect(page.getByRole("img", { name: /个人每日 Token 用量趋势/ })).toBeVisible();
  await capture("usage");
});
