// GitHub Pages finishing touches for dist/.
//
//   CNAME                 custom domain, so a Pages settings reset cannot drop it
//   404.html              copy of index.html; Pages serves it for unknown paths,
//                         which is how a BrowserRouter SPA deep-links on a static host
//   docs/<slug>/index.html  one copy per route, so known deep links answer 200
//   sitemap.txt           one URL per route
//   .nojekyll             stop Pages from running Jekyll over the output
import { copyFileSync, mkdirSync, readdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const site = resolve(fileURLToPath(import.meta.url), "../..");
const dist = join(site, "dist");
const origin = "https://factory.polds.dev";
const index = join(dist, "index.html");

const slugs = readdirSync(join(site, "src/content/docs"))
  .filter((f) => f.endsWith(".md"))
  .map((f) => f.replace(/\.md$/, "").toLowerCase())
  .sort((a, b) => (a === "readme" ? -1 : b === "readme" ? 1 : a.localeCompare(b)));

const routes = ["/", "/docs/", ...slugs.map((s) => `/docs/${s}/`)];
for (const route of routes.slice(1)) {
  const dir = join(dist, route);
  mkdirSync(dir, { recursive: true });
  copyFileSync(index, join(dir, "index.html"));
}

writeFileSync(join(dist, "CNAME"), "factory.polds.dev\n");
copyFileSync(index, join(dist, "404.html"));
writeFileSync(join(dist, ".nojekyll"), "");
writeFileSync(join(dist, "sitemap.txt"), routes.map((r) => origin + r).join("\n") + "\n");
console.log(`postbuild: ${routes.length} routes, CNAME, 404.html, sitemap.txt, .nojekyll`);
