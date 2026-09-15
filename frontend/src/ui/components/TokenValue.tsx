import { getIntlLocale } from "../../i18n";
import { formatTokenAmount } from "../formatters";

export type TokenValueProps = {
  value: number | null | undefined;
  suffix?: string;
  className?: string;
  emptyText?: string;
};

export function TokenValue({ value, suffix = "Token", className = "", emptyText = "—" }: TokenValueProps) {
  if (value === null || value === undefined) {
    return <span className={["token-value", className].filter(Boolean).join(" ")}>{emptyText}</span>;
  }
  const normalized = Number.isFinite(value) ? value : 0;
  const display = [formatTokenAmount(normalized), suffix].filter(Boolean).join(" ");
  return (
    <span
      className={["token-value", className].filter(Boolean).join(" ")}
      title={[new Intl.NumberFormat(getIntlLocale()).format(normalized), suffix].filter(Boolean).join(" ")}
    >
      {display}
    </span>
  );
}
