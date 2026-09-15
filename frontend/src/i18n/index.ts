import english from "./en/common.json";
import chinese from "./zh-CN/common.json";

export type Language = "en" | "zh-CN";
export const LANGUAGE_STORAGE_KEY = "cpa-ui-language";
const messages: Record<string, string> = { ...english };
const chineseMessages: Record<string, string> = { ...chinese };

function supported(value: string | null | undefined): value is Language {
  return value === "en" || value === "zh-CN";
}

export function resolveLanguage(): Language {
  if (typeof window === "undefined") return "en";
  const parameter = new URL(window.location.href).searchParams.get("lang");
  if (supported(parameter)) return parameter;
  // A host cookie also shares the preference between the three Vite ports.
  try {
    const cookie = document.cookie.split("; ").find((item) => item.startsWith(`${LANGUAGE_STORAGE_KEY}=`))?.split("=")[1];
    if (supported(cookie)) return cookie;
  } catch { /* Cookies may be disabled. */ }
  try {
    const stored = window.localStorage.getItem(LANGUAGE_STORAGE_KEY);
    if (supported(stored)) return stored;
  } catch { /* Local storage may be disabled. */ }
  // English is the product default, independently of the browser's language.
  return "en";
}

let language = resolveLanguage();

export function getLanguage(): Language { return language; }
export function getIntlLocale(): string { return language === "en" ? "en-US" : "zh-CN"; }

export function rememberLanguage(next: Language) {
  language = next;
  try { window.localStorage.setItem(LANGUAGE_STORAGE_KEY, next); } catch { /* Optional persistence. */ }
  try {
    document.cookie = `${LANGUAGE_STORAGE_KEY}=${next}; Path=/; Max-Age=31536000; SameSite=Lax${window.location.protocol === "https:" ? "; Secure" : ""}`;
  } catch { /* The URL remains a fallback when persistence is unavailable. */ }
}

/** Catalog IDs are stable. Wording changes belong in the locale catalogs. */
export function t(key: string, values: readonly (string | number | null | undefined)[] = []): string {
  const message = (language === "zh-CN" ? chineseMessages[key] : undefined) ?? messages[key] ?? key;
  return message.replace(/\{(\d+)\}/g, (match, index: string) => String(values[Number(index)] ?? match));
}

export function registerMessages(en: Readonly<Record<string, string>>, zh: Readonly<Record<string, string>>) {
  Object.assign(messages, en);
  Object.assign(chineseMessages, zh);
}

export function languageURL(next: Language): string {
  const url = new URL(window.location.href);
  url.searchParams.set("lang", next);
  return url.href;
}

export function initializeLanguage() {
  const selected = resolveLanguage();
  rememberLanguage(selected);
  document.documentElement.lang = selected;
  const page = document.documentElement.dataset.brandPage;
  if (page) document.title = t(page);
}
