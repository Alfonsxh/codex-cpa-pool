import { t } from "../../i18n";
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent
} from "react";

const OPEN_EVENT = "cpa:legacy-usage-variable-open";

export type LegacyUsageMultiSelectOption = {
  value: string;
  label: string;
};

export type LegacyUsageMultiSelectProps = {
  id: string;
  label: string;
  allLabel: string;
  searchPlaceholder: string;
  options: LegacyUsageMultiSelectOption[];
  value: string[];
  onChange: (value: string[]) => void;
  loading?: boolean;
  error?: boolean;
};

/** Frozen v1 Token Dashboard variable DOM and click state machine. */
export function LegacyUsageMultiSelect({
  id,
  label,
  allLabel,
  searchPlaceholder,
  options,
  value,
  onChange,
  loading = false,
  error = false
}: LegacyUsageMultiSelectProps) {
  const instanceId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const menuId = `${id}-menu`;

  const matchingOptions = useMemo(() => {
    const normalized = search.trim().toLocaleLowerCase();
    if (!normalized) return options;
    return options.filter((option) => option.label.toLocaleLowerCase().includes(normalized));
  }, [options, search]);

  useEffect(() => {
    const closeOther = (event: Event) => {
      if (!(event instanceof CustomEvent) || event.detail === instanceId) return;
      setOpen(false);
    };
    const closeOutside = (event: PointerEvent) => {
      if (rootRef.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    document.addEventListener(OPEN_EVENT, closeOther);
    document.addEventListener("pointerdown", closeOutside);
    return () => {
      document.removeEventListener(OPEN_EVENT, closeOther);
      document.removeEventListener("pointerdown", closeOutside);
    };
  }, [instanceId]);

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();
  }, [open]);

  const toggleMenu = () => {
    if (open) {
      setOpen(false);
      return;
    }
    document.dispatchEvent(new CustomEvent(OPEN_EVENT, { detail: instanceId }));
    setOpen(true);
  };

  const selectAll = () => onChange([]);
  const selectValue = (event: ChangeEvent<HTMLInputElement>) => {
    const selected = new Set(value);
    if (event.currentTarget.checked) selected.add(event.currentTarget.value);
    else selected.delete(event.currentTarget.value);
    onChange([...selected]);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "Escape" || !open) return;
    event.preventDefault();
    setOpen(false);
    rootRef.current?.querySelector<HTMLButtonElement>("[data-variable-trigger]")?.focus();
  };

  const emptyMessage = loading
    ? t("common.loading_options")
    : error
      ? t("common.unable_to_load_options")
      : options.length
        ? t("common.no_matching_options")
        : t("common.no_options", [label]);

  return (
    <div
      ref={rootRef}
      className="usage-variable"
      id={id}
      data-variable={label === "CPA" ? "account" : "user"}
      onKeyDown={onKeyDown}
    >
      <span className="usage-variable-label">{label}</span>
      <button
        className="usage-variable-trigger"
        type="button"
        data-variable-trigger
        aria-expanded={open}
        aria-controls={menuId}
        onClick={toggleMenu}
      >
        <span data-variable-value>{selectionSummary(value, allLabel)}</span>
        <span className="usage-variable-caret" aria-hidden="true">⌄</span>
      </button>
      <div className="usage-variable-menu" id={menuId} data-variable-menu hidden={!open}>
        <label className="usage-variable-search">
          <span aria-hidden="true">⌕</span>
          <input
            ref={searchRef}
            type="search"
            data-variable-search
            placeholder={searchPlaceholder}
            autoComplete="off"
            value={search}
            onChange={(event) => setSearch(event.currentTarget.value)}
          />
        </label>
        <label className="usage-variable-option usage-variable-all">
          <input type="checkbox" data-variable-all checked={value.length === 0} onChange={selectAll} />
          <span>{allLabel}</span>
        </label>
        <div className="usage-variable-options" data-variable-options>
          {matchingOptions.length ? matchingOptions.map((option) => (
            <label className="usage-variable-option" data-variable-option key={option.value} title={option.label}>
              <input
                type="checkbox"
                data-variable-value-option
                value={option.value}
                checked={value.includes(option.value)}
                onChange={selectValue}
              />
              <span>{option.label}</span>
            </label>
          )) : <span className="usage-variable-empty">{emptyMessage}</span>}
        </div>
        <small className="usage-variable-hint">{t("common.select_multiple_options_the_chart_updates_immediately")}</small>
      </div>
    </div>
  );
}

function selectionSummary(selected: string[], allLabel: string) {
  if (!selected.length) return allLabel;
  if (selected.length <= 2) return selected.join("、");
  return t("common.selected", [selected.length]);
}
