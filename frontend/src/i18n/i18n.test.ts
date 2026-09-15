import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import common from "./en/common.json";
import admin from "./en/admin.json";
import usage from "./en/usage.json";
import "./admin";
import "./usage";
import zhCommon from "./zh-CN/common.json";
import zhAdmin from "./zh-CN/admin.json";
import zhUsage from "./zh-CN/usage.json";
const english = { ...common, ...admin, ...usage };
const chinese: Record<string, string> = { ...zhCommon, ...zhAdmin, ...zhUsage };
import { getLanguage, initializeLanguage, LANGUAGE_STORAGE_KEY, languageURL, rememberLanguage, resolveLanguage, t } from "./index";
import { tokenReadableParts } from "../ui/formatters";

function clearPreference() {
  window.localStorage.clear();
  document.cookie = `${LANGUAGE_STORAGE_KEY}=; Path=/; Max-Age=0`;
  window.history.replaceState({}, "", "/");
}

beforeEach(clearPreference);
afterEach(() => { clearPreference(); vi.restoreAllMocks(); });

describe("English-first language preferences", () => {
  it("defaults to English even for a Chinese browser and ignores unsupported preferences", () => {
    expect(resolveLanguage()).toBe("en");
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, "fr");
    document.cookie = `${LANGUAGE_STORAGE_KEY}=invalid; Path=/`;
    expect(resolveLanguage()).toBe("en");
  });

  it("persists the choice, shares the host cookie across app ports, and keeps the current route", () => {
    rememberLanguage("zh-CN");
    expect(getLanguage()).toBe("zh-CN");
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("zh-CN");
    // An older localStorage value on another Vite origin must not win.
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, "en");
    expect(resolveLanguage()).toBe("zh-CN");
    window.history.replaceState({}, "", "/admin/configuration?section=quota#limits");
    const url = new URL(languageURL("en"));
    expect(url.pathname).toBe("/admin/configuration");
    expect(url.searchParams.get("section")).toBe("quota");
    expect(url.searchParams.get("lang")).toBe("en");
    expect(url.hash).toBe("#limits");
    window.history.replaceState({}, "", url);
    expect(resolveLanguage()).toBe("en");
  });

  it("uses the explicit URL even when browser persistence is blocked", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("blocked"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("blocked"); });
    window.history.replaceState({}, "", "/usage/?lang=zh-CN");
    initializeLanguage();
    expect(getLanguage()).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
  });

  it("sets the document language and title and preserves interpolation data", () => {
    document.documentElement.dataset.brandPage = "common.admin_2";
    initializeLanguage();
    expect(document.documentElement.lang).toBe("en");
    expect(document.title).toBe("Admin");
    expect(t("usage.switch_to", ["用户 <account> {1}"])).toBe("Switch to 用户 <account> {1}");
    expect(t("\u5df2\u4fdd\u5b58 2 \u9879\u914d\u7f6e")).toBe("已保存 2 项配置");
    expect(t("\u5b98\u65b9\u989d\u5ea6\u8017\u5c3d")).toBe("官方额度耗尽"); // Unknown upstream text is preserved.
    expect(t("Low \u63a8\u7406\u5f3a\u5ea6\u500d\u7387")).toBe("Low 推理强度倍率");
    rememberLanguage("zh-CN");
    expect(t("usage.switch_to", ["account-a"])).toBe("切换到 account-a");
    expect(t("\u5df2\u4fdd\u5b58 2 \u9879\u914d\u7f6e")).toBe("已保存 2 项配置");
  });

  it("uses English token units without changing exact counts", () => {
    rememberLanguage("en");
    const en = tokenReadableParts(15_000_000);
    rememberLanguage("zh-CN");
    const zh = tokenReadableParts(15_000_000);
    expect(en).toMatchObject({ state: "ready", exact: "15,000,000 Token", compact: "15 M Token", localized: "" });
    expect(zh).toMatchObject({ state: "ready", exact: "15,000,000 Token", compact: "15 M Token", localized: "1,500 万 Token" });
  });
});

it("has complete English translations with matching interpolation placeholders", () => {
  expect(Object.keys(english).sort()).toEqual(Object.keys(chinese).sort());
  for (const [key, value] of Object.entries(english)) {
    expect(key).toMatch(/^[a-z][a-z0-9_.]*$/);
    expect(value.trim(), key).not.toBe("");
    expect(value, key).not.toMatch(/\p{Script=Han}/u);
    expect([...value.matchAll(/\{\d+\}/g)].map(([token]) => token).sort(), key)
      .toEqual([...chinese[key].matchAll(/\{\d+\}/g)].map(([token]) => token).sort());
  }
  const root = path.resolve(import.meta.dirname, "..");
  for (const file of readdirSync(root, { recursive: true }) as string[]) {
    if (!/\.tsx?$/.test(file) || /(?:test|i18n)/.test(file)) continue;
    const source = readFileSync(path.join(root, file), "utf8");
    for (const match of source.matchAll(/\bt\(("(?:[^"\\]|\\.)*")/g)) {
      const key = JSON.parse(match[1]) as string;
      expect(english, `${file}: ${key}`).toHaveProperty([key]);
    }
  }
});

it("trusts localized configuration metadata and preserves stored values", async () => {
  const { readConfiguration } = await import("../api/configuration");
  const payload = { groups: [{ name: "品牌与身份", description: "品牌、域名和客户端配置。", fields: [{
    key: "branding.product_name", label: "产品名称", description: "页面产品名称。", value: "产品设计", default: "产品设计", unit: "倍",
    choices: [{ value: "原始值", label: "已启用" }]
  }] }] };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(payload))));
  rememberLanguage("en");
  try {
    const result = await readConfiguration();
    expect(result.groups[0].name).toBe("品牌与身份");
    expect(result.groups[0].fields[0]).toMatchObject({
      key: "branding.product_name", label: "产品名称", description: "页面产品名称。", value: "产品设计", default: "产品设计", unit: "倍",
      choices: [{ value: "原始值", label: "已启用" }]
    });
  } finally { vi.unstubAllGlobals(); }
});

it("sends the chosen language and preserves backend notices and arbitrary payloads", async () => {
  const { apiRequest } = await import("../api/client");
  const payload = { message: "已保存 2 项配置", name: "可用", output: "账号不存在", details: { message: "已保存 2 项配置" } };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(payload))));
  rememberLanguage("en");
  try {
    expect(await apiRequest("/admin/api/test")).toEqual(payload);
    const headers = new Headers(vi.mocked(fetch).mock.calls[0][1]?.headers);
    expect(headers.get("Accept-Language")).toBe("en");
  } finally { vi.unstubAllGlobals(); }
});
