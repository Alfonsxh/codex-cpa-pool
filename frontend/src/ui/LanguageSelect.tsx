import { getLanguage, languageURL, rememberLanguage, t, type Language } from "../i18n";

export function LanguageSelect({ className = "" }: { className?: string }) {
  return (
    <label className={`language-select ${className}`.trim()}>
      <span aria-hidden="true">◎</span>
      <select
        aria-label={t("common.language")}
        title={t("common.change_language")}
        value={getLanguage()}
        onChange={(event) => {
          const next = event.target.value as Language;
          rememberLanguage(next);
          // Reload this route so module-level labels, schemas, chart formatters
          // and server notices all use one language. Authentication is retained.
          window.location.assign(languageURL(next));
        }}
      >
        <option value="en" lang="en">English</option>
        <option value="zh-CN" lang="zh-CN">简体中文</option>
      </select>
    </label>
  );
}
