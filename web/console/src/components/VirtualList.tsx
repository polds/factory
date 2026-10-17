import { useVirtualizer } from "@tanstack/react-virtual";
import { useRef, type ReactNode } from "react";

export function VirtualList<T>({
  items,
  estimateSize = 36,
  getKey,
  renderItem,
  label,
  empty,
}: {
  items: readonly T[];
  estimateSize?: number;
  getKey: (item: T, index: number) => string;
  renderItem: (item: T, index: number) => ReactNode;
  label: string;
  empty?: ReactNode;
}) {
  const parentRef = useRef<HTMLDivElement>(null);
  // useVirtualizer returns functions React Compiler cannot memoize.
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual documents this skip
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => estimateSize,
    measureElement: (element) => {
      const height = element instanceof HTMLElement ? element.offsetHeight : 0;
      return height > 0 ? height : estimateSize;
    },
    overscan: 8,
    initialRect: { width: 640, height: 320 },
  });

  if (items.length === 0 && empty) return <>{empty}</>;

  return (
    <div ref={parentRef} className="virtual-list" role="list" aria-label={label} tabIndex={0}>
      <div className="virtual-list-inner" style={{ height: virtualizer.getTotalSize() }}>
        {virtualizer.getVirtualItems().map((virtualRow) => {
          const item = items[virtualRow.index];
          if (item === undefined) return null;
          return (
            <div
              key={getKey(item, virtualRow.index)}
              role="listitem"
              data-index={virtualRow.index}
              ref={virtualizer.measureElement}
              className="virtual-row"
              style={{ transform: `translateY(${virtualRow.start}px)` }}
            >
              {renderItem(item, virtualRow.index)}
            </div>
          );
        })}
      </div>
    </div>
  );
}
