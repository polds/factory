import { cp, mkdir, rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

// go:embed only accepts files under the package directory, and it rejects a
// symlink. Vite writes web/console/dist; this copies that tree to
// internal/console/dist so `go test -tags console` and the image build see it.
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
const dist = path.join(repoRoot, "web", "console", "dist");
const embed = path.join(repoRoot, "internal", "console", "dist");
const expectedSuffix = path.join("internal", "console", "dist");

if (!embed.endsWith(expectedSuffix)) {
  throw new Error(`refusing to replace ${embed}`);
}

await rm(embed, { recursive: true, force: true });
await mkdir(embed, { recursive: true });
await cp(dist, embed, { recursive: true });
