import { t } from "../../i18n";
import { Empty, Table, type TableProps } from "antd";
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

export type AdminTableProps<RecordType extends object> = Omit<TableProps<RecordType>, "locale" | "scroll"> & {
  emptyText?: ReactNode;
  emptyAction?: ReactNode;
  minWidth?: number | string;
  maxBodyHeight?: number | string;
  fillAvailable?: boolean;
  locale?: TableProps<RecordType>["locale"];
  scroll?: TableProps<RecordType>["scroll"];
};

export function AdminTable<RecordType extends object>({
  className = "",
  emptyText = t("common.no_data"),
  emptyAction,
  fillAvailable = false,
  locale,
  maxBodyHeight = "min(54vh, 560px)",
  minWidth = 900,
  pagination = false,
  scroll,
  size = "middle",
  ...props
}: AdminTableProps<RecordType>) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const [measuredBodyHeight, setMeasuredBodyHeight] = useState<number | null>(null);
  const [scrollState, setScrollState] = useState({ overflow: false, top: false, bottom: false });
  const measure = useCallback(() => {
    if (!fillAvailable || !viewportRef.current) return;
    const viewport = viewportRef.current;
    const header = viewport.querySelector<HTMLElement>(".ant-table-header, .ant-table-thead");
    const headerHeight = Math.ceil(header?.getBoundingClientRect().height ?? 0);
    const next = Math.max(120, Math.floor(viewport.clientHeight - headerHeight));
    setMeasuredBodyHeight((current) => current === next ? current : next);
  }, [fillAvailable]);
  const updateScrollState = useCallback(() => {
    const body = viewportRef.current?.querySelector<HTMLElement>(".ant-table-body");
    if (!body) return;
    const maximum = Math.max(0, body.scrollHeight - body.clientHeight);
    const horizontalMaximum = Math.max(0, body.scrollWidth - body.clientWidth);
    const next = {
      overflow: maximum > 1 || horizontalMaximum > 1,
      top: body.scrollTop > 1,
      bottom: body.scrollTop < maximum - 1
    };
    setScrollState((current) => current.overflow === next.overflow && current.top === next.top && current.bottom === next.bottom ? current : next);
  }, []);
  useLayoutEffect(() => {
    measure();
  }, [measure, props.dataSource]);
  useEffect(() => {
    if (!fillAvailable) return;
    const viewport = viewportRef.current;
    if (!viewport) return;
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => {
      measure();
      updateScrollState();
    });
    resize?.observe(viewport);
    if (viewport.parentElement) resize?.observe(viewport.parentElement);
    window.addEventListener("resize", measure);
    const animation = window.requestAnimationFrame(measure);
    return () => {
      resize?.disconnect();
      window.removeEventListener("resize", measure);
      window.cancelAnimationFrame(animation);
    };
  }, [fillAvailable, measure, updateScrollState]);
  useEffect(() => {
    const body = viewportRef.current?.querySelector<HTMLElement>(".ant-table-body");
    if (!body) return;
    updateScrollState();
    body.addEventListener("scroll", updateScrollState, { passive: true });
    return () => body.removeEventListener("scroll", updateScrollState);
  }, [measuredBodyHeight, props.dataSource, updateScrollState]);
  const configuredEmpty = typeof locale?.emptyText === "function" ? locale.emptyText() : locale?.emptyText;
  const emptyState = configuredEmpty ?? (
    <Empty description={emptyText} image={Empty.PRESENTED_IMAGE_SIMPLE}>
      {emptyAction}
    </Empty>
  );
  if (!props.loading && Array.isArray(props.dataSource) && props.dataSource.length === 0 && emptyAction) {
    return <div className={["admin-table-empty", className].filter(Boolean).join(" ")}>{emptyState}</div>;
  }

  return (
    <div
      ref={viewportRef}
      className={[
        "admin-table-viewport",
        fillAvailable ? "admin-table-fill" : "",
        scrollState.top ? "can-scroll-up" : "",
        scrollState.bottom ? "can-scroll-down" : "",
        className
      ].filter(Boolean).join(" ")}
      data-scroll-overflow={scrollState.overflow ? "true" : "false"}
      tabIndex={scrollState.overflow ? 0 : undefined}
    >
      <Table<RecordType>
        {...props}
        className="admin-data-table"
        locale={{ ...locale, emptyText: emptyState }}
        pagination={pagination}
        scroll={{ x: minWidth, y: fillAvailable ? measuredBodyHeight ?? maxBodyHeight : maxBodyHeight, ...scroll }}
        size={size}
      />
    </div>
  );
}
