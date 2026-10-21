import { httpsHref } from "../../lib/format";

// An https URL is a link. Anything else (http, javascript, relative, empty) stays text.
export function HttpsText({ url, label }: { url: string; label: string }) {
  const href = httpsHref(url);
  if (!href) return <span className="mono">{url}</span>;
  return (
    <a href={href} target="_blank" rel="noreferrer">
      {label}
    </a>
  );
}
