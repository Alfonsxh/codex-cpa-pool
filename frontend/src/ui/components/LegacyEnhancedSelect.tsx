import { t } from "../../i18n";
import {
  type CSSProperties,
  Fragment,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent
} from "react";
import { createPortal } from "react-dom";

const OPEN_EVENT = "cpa:legacy-enhanced-select-open";

export type LegacyEnhancedSelectOption<Value extends string> = {
  value: Value;
  label: string;
  group?: string;
};

export type LegacyEnhancedSelectProps<Value extends string> = {
  id?: string;
  label: string;
  value: Value;
  options: Array<LegacyEnhancedSelectOption<Value>>;
  onChange: (value: Value) => void;
  disabled?: boolean;
  autoFocus?: boolean;
  required?: boolean;
  title?: string;
  ariaInvalid?: boolean;
  ariaDescribedBy?: string;
};

/** React implementation of the frozen v1 enhanceSelect() DOM and state machine. */
export function LegacyEnhancedSelect<Value extends string>({
  id = "",
  label,
  value,
  options,
  onChange,
  disabled = false,
  autoFocus = false,
  required = false,
  title,
  ariaInvalid,
  ariaDescribedBy
}: LegacyEnhancedSelectProps<Value>) {
  const [open, setOpen] = useState(false);
  const [menuStyle, setMenuStyle] = useState<CSSProperties>({});
  const instanceId = useId();
  const controlId = id || `enhanced-select-${instanceId.replaceAll(":", "")}`;
  const wrapperRef = useRef<HTMLSpanElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLSpanElement>(null);
  const menuId = `${controlId}-menu`;
  const selected = options.find((option) => option.value === value) ?? options[0];
  const selectedLabel = selected?.label || t("common.select_an_option");

  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);

  useEffect(() => {
    const closeOtherSelect = (event: Event) => {
      if (!(event instanceof CustomEvent) || event.detail === instanceId) return;
      setOpen(false);
    };
    const closeFromOutside = (event: PointerEvent) => {
      if (wrapperRef.current?.contains(event.target as Node) || menuRef.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    document.addEventListener(OPEN_EVENT, closeOtherSelect);
    document.addEventListener("pointerdown", closeFromOutside);
    return () => {
      document.removeEventListener(OPEN_EVENT, closeOtherSelect);
      document.removeEventListener("pointerdown", closeFromOutside);
    };
  }, [instanceId]);

  useLayoutEffect(() => {
    if (!open) return;
    const updateMenuPosition = () => {
      const trigger = triggerRef.current;
      if (!trigger) return;
      const rect = trigger.getBoundingClientRect();
      const viewportWidth = document.documentElement.clientWidth || window.innerWidth;
      const viewportHeight = document.documentElement.clientHeight || window.innerHeight;
      const edge = 8;
      const gap = 7;
      const width = Math.min(Math.max(rect.width, 170), Math.max(1, viewportWidth - edge * 2));
      const left = Math.min(Math.max(edge, rect.right - width), Math.max(edge, viewportWidth - width - edge));
      const below = viewportHeight - rect.bottom - edge;
      const above = rect.top - edge;
      const openAbove = below < 180 && above > below;
      const maxHeight = Math.max(96, Math.min(260, (openAbove ? above : below) - gap));
      setMenuStyle({
        left,
        width,
        maxHeight,
        top: openAbove ? undefined : rect.bottom + gap,
        bottom: openAbove ? viewportHeight - rect.top + gap : undefined
      });
    };
    updateMenuPosition();
    menuRef.current?.querySelector<HTMLButtonElement>('[role="option"][aria-selected="true"]')?.focus();
    window.addEventListener("resize", updateMenuPosition);
    window.addEventListener("scroll", updateMenuPosition, true);
    return () => {
      window.removeEventListener("resize", updateMenuPosition);
      window.removeEventListener("scroll", updateMenuPosition, true);
    };
  }, [open]);

  const openMenu = () => {
    if (disabled) return;
    document.dispatchEvent(new CustomEvent(OPEN_EVENT, { detail: instanceId }));
    setOpen(true);
  };

  const closeAndFocusTrigger = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };

  const selectOption = (nextValue: Value) => {
    if (disabled) return;
    const previous = value;
    closeAndFocusTrigger();
    if (nextValue !== previous || nextValue === "custom") onChange(nextValue);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape") {
      closeAndFocusTrigger();
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    if (!open) {
      openMenu();
      return;
    }
    const optionButtons = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>("[data-select-value]") ?? []
    );
    const current = Math.max(0, optionButtons.indexOf(document.activeElement as HTMLButtonElement));
    const next = event.key === "Home"
      ? 0
      : event.key === "End"
        ? optionButtons.length - 1
        : Math.min(optionButtons.length - 1, Math.max(0, current + (event.key === "ArrowDown" ? 1 : -1)));
    optionButtons[next]?.focus();
  };

  const handleFocusOut = (event: FocusEvent<HTMLElement>) => {
    if (wrapperRef.current?.contains(event.relatedTarget as Node | null) || menuRef.current?.contains(event.relatedTarget as Node | null)) return;
    setOpen(false);
  };

  const menu = open && !disabled ? (
    <span
      ref={menuRef}
      className="enhanced-select-menu enhanced-select-menu-portal"
      id={menuId}
      role="listbox"
      aria-label={label}
      style={menuStyle}
      onKeyDown={handleKeyDown}
      onBlur={handleFocusOut}
    >
      {options.map((option, index) => (
        <Fragment key={option.value}>
          {option.group && option.group !== options[index - 1]?.group
            ? <span className="enhanced-select-group" role="presentation">{option.group}</span>
            : null}
          <button
            type="button"
            role="option"
            data-select-value={option.value}
            aria-selected={option.value === value}
            onClick={() => selectOption(option.value)}
          >
            <span className="enhanced-select-check" aria-hidden="true">{option.value === value ? "✓" : ""}</span>
            <span>{option.label}</span>
          </button>
        </Fragment>
      ))}
    </span>
  ) : null;

  return (
    <>
      <select
        id={controlId}
        className="enhanced-select-native"
        value={value}
        tabIndex={-1}
        aria-hidden="true"
        disabled={disabled}
        required={required}
        onChange={(event) => onChange(event.currentTarget.value as Value)}
      >
        {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
      <span
        ref={wrapperRef}
        className="enhanced-select"
        onKeyDown={handleKeyDown}
        onBlur={handleFocusOut}
      >
        <button
          ref={triggerRef}
          className="enhanced-select-trigger"
          type="button"
          aria-label={`${label}：${selectedLabel}`}
          aria-invalid={ariaInvalid}
          aria-describedby={ariaDescribedBy}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={menuId}
          title={title || selectedLabel}
          disabled={disabled}
          autoFocus={autoFocus}
          onClick={() => open ? setOpen(false) : openMenu()}
        >
          <span data-enhanced-select-value>{selectedLabel}</span>
          <span className="enhanced-select-caret" aria-hidden="true">⌄</span>
        </button>
      </span>
      {menu && typeof document !== "undefined" ? createPortal(menu, document.body) : menu}
    </>
  );
}
