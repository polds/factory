import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The site is served from the root of factory.polds.dev, so base stays "/".
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: { host: "127.0.0.1" },
  test: { environment: "node", include: ["src/**/*.test.ts"] },
});
