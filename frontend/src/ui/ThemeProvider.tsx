import { t, getLanguage } from "../i18n";
import { App as AntApp, ConfigProvider, theme as antTheme, type ThemeConfig } from "antd";
import enUS from "antd/locale/en_US";
import dayjs from "dayjs";
import "dayjs/locale/zh-cn";
import zhCN from "antd/locale/zh_CN";
import { createContext, useContext, useLayoutEffect, useMemo, useState } from "react";

export type ThemeMode = "light" | "dark";

export const THEME_STORAGE_KEY = "cpa-ui-theme";
export const LEGACY_THEME_STORAGE_KEY = "cpa-admin-theme";

type ThemeContextValue = {
  theme: ThemeMode;
  setTheme: (theme: ThemeMode) => void;
  toggleTheme: () => void;
};

const ThemeContext = createContext<ThemeContextValue>({
  theme: "light",
  setTheme: () => undefined,
  toggleTheme: () => undefined
});

export function resolveInitialTheme(): ThemeMode {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY)
      || window.localStorage.getItem(LEGACY_THEME_STORAGE_KEY);
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // Storage can be unavailable in hardened or private browser contexts.
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyDocumentTheme(theme: ThemeMode) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
  const favicon = document.querySelector<HTMLLinkElement>('link[rel~="icon"]');
  if (favicon?.href.includes("codex-cpa-pool-favicon")) {
    favicon.href = `/portal/assets/codex-cpa-pool-favicon${theme === "dark" ? "-dark" : ""}.svg`;
  }
}

dayjs.locale(getLanguage() === "en" ? "en" : "zh-cn");

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setThemeState] = useState<ThemeMode>(() => {
    const initial = resolveInitialTheme();
    applyDocumentTheme(initial);
    return initial;
  });

  useLayoutEffect(() => applyDocumentTheme(theme), [theme]);

  const value = useMemo<ThemeContextValue>(() => ({
    theme,
    setTheme: (nextTheme) => {
      try { window.localStorage.setItem(THEME_STORAGE_KEY, nextTheme); } catch { /* ignore unavailable storage */ }
      setThemeState(nextTheme);
    },
    toggleTheme: () => {
      const nextTheme = theme === "dark" ? "light" : "dark";
      try { window.localStorage.setItem(THEME_STORAGE_KEY, nextTheme); } catch { /* ignore unavailable storage */ }
      setThemeState(nextTheme);
    }
  }), [theme]);

  const componentTheme = useMemo<ThemeConfig>(() => {
    const dark = theme === "dark";
    const colors = {
      accent: dark ? "#8d9bf1" : "#6374d8",
      background: dark ? "#0d121c" : "#f3f6fb",
      container: dark ? "#151b28" : "#ffffff",
      elevated: dark ? "#1a2232" : "#ffffff",
      surface: dark ? "#1a2232" : "#f8faff",
      text: dark ? "#edf1fb" : "#1d2437",
      muted: dark ? "#a4aec2" : "#667085",
      faint: dark ? "#7f8ba3" : "#8b95a7",
      border: dark ? "#263146" : "#e3e8f1",
      borderStrong: dark ? "#344157" : "#d2d9e6",
      hover: dark ? "#202944" : "#f4f5ff"
    };
    return {
      algorithm: dark ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
      token: {
        colorPrimary: colors.accent,
        colorInfo: colors.accent,
        colorSuccess: dark ? "#73d3a5" : "#3f8f67",
        colorWarning: dark ? "#e0b55f" : "#9b6718",
        colorError: dark ? "#ef8e94" : "#b5474f",
        colorBgBase: colors.background,
        colorBgLayout: colors.background,
        colorBgContainer: colors.container,
        colorBgElevated: colors.elevated,
        colorFillAlter: colors.surface,
        colorTextBase: colors.text,
        colorTextSecondary: colors.muted,
        colorTextTertiary: colors.faint,
        colorBorder: colors.borderStrong,
        colorBorderSecondary: colors.border,
        borderRadius: 9,
        borderRadiusLG: 16,
        controlHeight: 36,
        controlHeightSM: 30,
        fontSize: 13,
        fontSizeSM: 12,
        fontFamily: '"Geist", "Plus Jakarta Sans", "Avenir Next", "Segoe UI", "PingFang SC", sans-serif'
      },
      components: {
        Button: {
          fontWeight: 650,
          contentFontSize: 12,
          contentFontSizeSM: 10,
          paddingInline: 12,
          paddingInlineSM: 9,
          defaultShadow: "none",
          primaryShadow: "none",
          dangerShadow: "none"
        },
        Card: {
          headerBg: colors.container,
          headerFontSize: 16,
          headerHeight: 58,
          headerPadding: 17
        },
        Input: {
          activeBorderColor: colors.accent,
          hoverBorderColor: colors.accent,
          activeShadow: `0 0 0 3px ${dark ? "rgb(141 155 241 / 14%)" : "rgb(99 116 216 / 14%)"}`
        },
        Pagination: {
          itemSize: 34,
          itemSizeSM: 30,
          itemActiveBg: colors.accent,
          itemActiveColor: "#ffffff",
          itemActiveColorHover: "#ffffff"
        },
        Select: {
          activeBorderColor: colors.accent,
          hoverBorderColor: colors.accent,
          optionActiveBg: colors.hover,
          optionSelectedBg: dark ? "#262d51" : "#eef0ff",
          optionFontSize: 12,
          optionHeight: 36
        },
        Table: {
          headerBg: colors.surface,
          headerColor: colors.faint,
          borderColor: colors.border,
          rowHoverBg: colors.hover,
          cellPaddingBlockMD: 13,
          cellPaddingInlineMD: 15,
          cellPaddingBlockSM: 10,
          cellPaddingInlineSM: 12,
          cellFontSizeMD: 12,
          cellFontSizeSM: 11,
          headerBorderRadius: 0
        }
      }
    };
  }, [theme]);

  return (
    <ThemeContext.Provider value={value}>
      <ConfigProvider locale={getLanguage() === "en" ? enUS : zhCN} theme={componentTheme} button={{ autoInsertSpace: false }}>
        <AntApp>{children}</AntApp>
      </ConfigProvider>
    </ThemeContext.Provider>
  );
}

export function useTheme() {
  return useContext(ThemeContext);
}

export function ThemeToggle({ className = "" }: { className?: string }) {
  const { theme, toggleTheme } = useTheme();
  const nextLabel = theme === "dark" ? t("common.light") : t("common.dark");
  return (
    <button
      className={`theme-toggle ${className}`.trim()}
      type="button"
      aria-label={t("common.switch_to_theme", [nextLabel])}
      title={t("common.switch_theme")}
      onClick={toggleTheme}
    >
      <span className="theme-toggle-icon" aria-hidden="true">
        {theme === "dark" ? "☀" : "☾"}
      </span>
      <span className="theme-toggle-label">{nextLabel}</span>
    </button>
  );
}
