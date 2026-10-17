import { ChevronDown, ChevronRight } from "lucide-react";
import { useRef, useState, type KeyboardEvent } from "react";

type TreeNode = {
  name: string;
  path: string;
  children?: TreeNode[];
};

type Row = {
  name: string;
  path: string;
  depth: number;
  folder: boolean;
};

function buildTree(paths: readonly string[]): TreeNode[] {
  const roots: TreeNode[] = [];
  for (const filePath of paths) {
    const parts = filePath.split("/").filter((part) => part.length > 0);
    let level = roots;
    let accumulated = "";
    parts.forEach((part, index) => {
      accumulated = accumulated ? `${accumulated}/${part}` : part;
      const last = index === parts.length - 1;
      let node = level.find((item) => item.name === part);
      if (!node) {
        node = last
          ? { name: part, path: accumulated }
          : { name: part, path: accumulated, children: [] };
        level.push(node);
      } else if (!last && !node.children) {
        node.children = [];
      }
      if (!last && node.children) level = node.children;
    });
  }
  return roots;
}

function visibleRows(nodes: readonly TreeNode[], collapsed: ReadonlySet<string>, depth = 0): Row[] {
  const rows: Row[] = [];
  for (const node of nodes) {
    const folder = Boolean(node.children);
    rows.push({ name: node.name, path: node.path, depth, folder });
    if (folder && node.children && !collapsed.has(node.path)) {
      rows.push(...visibleRows(node.children, collapsed, depth + 1));
    }
  }
  return rows;
}

function parentPath(path: string): string | null {
  const index = path.lastIndexOf("/");
  if (index < 0) return null;
  return path.slice(0, index);
}

export function FileTree({
  paths,
  selected,
  errorPaths,
  onSelect,
}: {
  paths: readonly string[];
  selected: string | null;
  errorPaths: ReadonlySet<string>;
  onSelect: (path: string) => void;
}) {
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const [focused, setFocused] = useState(selected ?? paths[0] ?? "");
  const [trackedSelection, setTrackedSelection] = useState(selected);
  if (selected !== trackedSelection) {
    setTrackedSelection(selected);
    if (selected) setFocused(selected);
  }
  const itemRefs = useRef(new Map<string, HTMLDivElement>());
  const tree = buildTree(paths);
  const rows = visibleRows(tree, collapsed);

  function toggle(path: string) {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }

  function moveTo(index: number) {
    const row = rows[index];
    if (!row) return;
    setFocused(row.path);
    if (!row.folder) onSelect(row.path);
    queueMicrotask(() => itemRefs.current.get(row.path)?.focus());
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>, index: number) {
    const row = rows[index];
    if (!row) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      moveTo(Math.min(rows.length - 1, index + 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveTo(Math.max(0, index - 1));
    } else if (event.key === "Home") {
      event.preventDefault();
      moveTo(0);
    } else if (event.key === "End") {
      event.preventDefault();
      moveTo(rows.length - 1);
    } else if (event.key === "ArrowRight") {
      event.preventDefault();
      if (row.folder && collapsed.has(row.path)) toggle(row.path);
      else moveTo(Math.min(rows.length - 1, index + 1));
    } else if (event.key === "ArrowLeft") {
      event.preventDefault();
      if (row.folder && !collapsed.has(row.path)) {
        toggle(row.path);
        return;
      }
      const parent = parentPath(row.path);
      const parentIndex = parent ? rows.findIndex((item) => item.path === parent) : -1;
      if (parentIndex >= 0) moveTo(parentIndex);
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (row.folder) toggle(row.path);
      else onSelect(row.path);
    }
  }

  return (
    <div
      role="tree"
      aria-label="Definition files"
      className="border border-[var(--border)] bg-[var(--card)] py-1"
    >
      {rows.map((row, index) => {
        const expanded = row.folder ? !collapsed.has(row.path) : undefined;
        return (
          <div
            key={row.path}
            role="treeitem"
            aria-level={row.depth + 1}
            aria-expanded={expanded}
            aria-selected={row.folder ? undefined : row.path === selected}
            aria-label={row.path}
            tabIndex={row.path === focused || (focused === "" && index === 0) ? 0 : -1}
            ref={(node) => {
              if (node) itemRefs.current.set(row.path, node);
              else itemRefs.current.delete(row.path);
            }}
            className="flex cursor-pointer items-center gap-1 py-0.5 pr-2 text-sm hover:bg-[var(--muted)]"
            style={{ paddingLeft: `${0.4 + row.depth * 0.9}rem` }}
            onClick={() => {
              setFocused(row.path);
              if (row.folder) toggle(row.path);
              else onSelect(row.path);
            }}
            onKeyDown={(event) => onKeyDown(event, index)}
          >
            {row.folder ? (
              expanded ? (
                <ChevronDown className="size-3.5 shrink-0" aria-hidden />
              ) : (
                <ChevronRight className="size-3.5 shrink-0" aria-hidden />
              )
            ) : (
              <span className="inline-block size-3.5 shrink-0" aria-hidden />
            )}
            {errorPaths.has(row.path) ? (
              <span className="text-[var(--danger)]" aria-hidden>
                ●
              </span>
            ) : null}
            <span className={row.path === selected ? "font-semibold underline" : undefined}>
              {row.name}
            </span>
          </div>
        );
      })}
    </div>
  );
}
