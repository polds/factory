// Copy the repository docs into src/content/docs so Vite can bundle them.
//
//   README.md       -> src/content/docs/README.md
//   docs/*.md       -> src/content/docs/*.md
//
// The site renders the repository's own prose. Nothing is hand-maintained
// here; edit README.md and docs/ instead. Runs before `vite` and `vite build`.
import { cpSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const site = resolve(fileURLToPath(import.meta.url), "../..");
const repo = resolve(site, "../..");
const out = join(site, "src/content/docs");

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
cpSync(join(repo, "README.md"), join(out, "README.md"));
let count = 1;
for (const name of readdirSync(join(repo, "docs"))) {
  if (!name.endsWith(".md")) continue;
  cpSync(join(repo, "docs", name), join(out, name));
  count += 1;
}
console.log(`sync-docs: ${count} files -> ${out}`);
