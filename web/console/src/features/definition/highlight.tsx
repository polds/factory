import type { ReactNode } from "react";

function valueSpan(value: string): ReactNode {
  if (!value) return null;
  const commentAt = value.search(/\s#/);
  const main = commentAt >= 0 ? value.slice(0, commentAt) : value;
  const comment = commentAt >= 0 ? value.slice(commentAt) : "";
  const token = main.trim();
  let color = "var(--syn-str)";
  if (/^(true|false|null|-?\d+(\.\d+)?)$/.test(token)) color = "var(--syn-num)";
  return (
    <>
      <span style={{ color }}>{main}</span>
      {comment ? (
        <span className="italic" style={{ color: "var(--syn-comment)" }}>
          {comment}
        </span>
      ) : null}
    </>
  );
}

export function highlightYaml(line: string): ReactNode {
  const match = /^(\s*)(.*)$/.exec(line);
  const indent = match?.[1] ?? "";
  let rest = match?.[2] ?? "";
  if (rest.startsWith("#")) {
    return (
      <>
        {indent}
        <span className="italic" style={{ color: "var(--syn-comment)" }}>
          {rest}
        </span>
      </>
    );
  }
  let dash = "";
  if (rest.startsWith("- ")) {
    dash = "- ";
    rest = rest.slice(2);
  }
  const key = /^([\w.\-"/]+):(\s?)(.*)$/.exec(rest);
  if (key) {
    return (
      <>
        {indent}
        <span style={{ color: "var(--muted-foreground)" }}>{dash}</span>
        <span style={{ color: "var(--syn-key)" }}>{key[1]}</span>
        {":"}
        {key[2]}
        {valueSpan(key[3] ?? "")}
      </>
    );
  }
  return (
    <>
      {indent}
      <span style={{ color: "var(--muted-foreground)" }}>{dash}</span>
      {valueSpan(rest)}
    </>
  );
}
