import { useCallback, useEffect, useRef, useState } from "react";

export type LegacyToastKind = "success" | "error";

export type LegacyToastItem = {
  id: number;
  message: string;
  kind: LegacyToastKind;
};

let nextToastID = 1;

export function useLegacyToasts() {
  const [toasts, setToasts] = useState<LegacyToastItem[]>([]);
  const timers = useRef(new Map<number, number>());

  const dismissToast = useCallback((id: number) => {
    const timer = timers.current.get(id);
    if (timer !== undefined) window.clearTimeout(timer);
    timers.current.delete(id);
    setToasts((current) => current.filter((toast) => toast.id !== id));
  }, []);

  const showToast = useCallback((message: string, kind: LegacyToastKind = "success") => {
    const id = nextToastID++;
    setToasts((current) => [...current, { id, message, kind }]);
    const timer = window.setTimeout(() => dismissToast(id), 4_200);
    timers.current.set(id, timer);
  }, [dismissToast]);

  useEffect(() => () => {
    timers.current.forEach((timer) => window.clearTimeout(timer));
    timers.current.clear();
  }, []);

  return { toasts, showToast };
}

export function LegacyToastRegion({ toasts }: { toasts: LegacyToastItem[] }) {
  return (
    <div className="toast-region" aria-live="polite">
      {toasts.map((toast) => (
        <div className={`toast${toast.kind === "error" ? " error" : ""}`} key={toast.id}>
 {(toast.message)}
        </div>
      ))}
    </div>
  );
}
