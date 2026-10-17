import { useEffect, useRef } from "react";
import type { Diagnostic } from "../../api/types";
import { highlightYaml } from "./highlight";
import { lineCount, lineOffset } from "./model";
import "./editor.css";

export function CodeEditor({
  path,
  value,
  readOnly,
  diagnostics,
  currentLine,
  onLine,
  onChange,
  focusNonce,
}: {
  path: string;
  value: string;
  readOnly: boolean;
  diagnostics: readonly Diagnostic[];
  currentLine: number;
  onLine: (line: number) => void;
  onChange: (value: string) => void;
  focusNonce: number;
}) {
  const textarea = useRef<HTMLTextAreaElement>(null);
  const lines = value === "" ? [""] : value.split("\n");
  const messages = new Map<number, string[]>();
  for (const diagnostic of diagnostics) {
    if (diagnostic.file !== path) continue;
    const list = messages.get(diagnostic.line) ?? [];
    list.push(diagnostic.message);
    messages.set(diagnostic.line, list);
  }
  const label = `Contents of ${path}`;

  useEffect(() => {
    if (focusNonce === 0 || readOnly) return;
    const element = textarea.current;
    if (!element) return;
    const offset = lineOffset(element.value, currentLine);
    element.focus();
    element.setSelectionRange(offset, offset);
  }, [focusNonce, currentLine, readOnly, path]);

  function syncFromSelection(element: HTMLTextAreaElement) {
    const line = element.value.slice(0, element.selectionStart).split("\n").length;
    onLine(line);
  }

  return (
    <div className="def-editor">
      <ol className="def-gutter" aria-hidden="true">
        {lines.map((_, index) => {
          const line = index + 1;
          const note = messages.get(line)?.join("; ");
          return (
            <li key={line} data-current={currentLine === line ? "true" : "false"}>
              {note ? (
                <span className="def-mark" data-diagnostic-line={line} title={note}>
                  ●
                </span>
              ) : (
                <span className="def-mark" />
              )}
              {line}
            </li>
          );
        })}
      </ol>
      <div className="def-code">
        {readOnly ? (
          <div role="region" aria-label={label}>
            {lines.map((line, index) => {
              const number = index + 1;
              return (
                <div
                  key={number}
                  className="def-line"
                  data-current={currentLine === number ? "true" : "false"}
                  data-error={messages.has(number) ? "true" : "false"}
                  onClick={() => onLine(number)}
                >
                  {highlightYaml(line)}
                  {line === "" ? " " : null}
                </div>
              );
            })}
          </div>
        ) : (
          <>
            <div
              className="def-current-bar"
              style={{ top: `calc(0.5rem + ${(currentLine - 1) * 1.45}rem)` }}
            />
            <textarea
              ref={textarea}
              aria-label={label}
              spellCheck={false}
              wrap="off"
              value={value}
              style={{ height: `calc(${lineCount(value)} * var(--line))` }}
              onChange={(event) => {
                onChange(event.target.value);
                syncFromSelection(event.target);
              }}
              onClick={(event) => syncFromSelection(event.currentTarget)}
              onKeyUp={(event) => syncFromSelection(event.currentTarget)}
              onSelect={(event) => syncFromSelection(event.currentTarget)}
            />
          </>
        )}
      </div>
    </div>
  );
}
