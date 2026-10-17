import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Connect, Plugin } from "vite";
import { defineConfig } from "vitest/config";
import { demoAPIKey } from "./src/mocks/demo-key.ts";

const factories = [
  { id: "fac_payments", name: "payments", alias: "pay" },
  { id: "fac_platform", name: "platform" },
];

function demoApi(): Plugin {
  const handle: Connect.NextHandleFunction = (req, res, next) => {
    if (process.env.CONSOLE_DEMO !== "1") {
      next();
      return;
    }
    const url = req.url ?? "";
    if (!url.startsWith("/api/")) {
      next();
      return;
    }
    if (req.headers.authorization !== `Bearer ${demoAPIKey}`) {
      res.statusCode = 401;
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ title: "unauthorized", status: 401, detail: "unauthorized" }));
      return;
    }
    if (url === "/api/v1/factory" || url.startsWith("/api/v1/factory?")) {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify(factories));
      return;
    }
    res.statusCode = 404;
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ title: "not found", status: 404, detail: "not found" }));
  };
  return {
    name: "console-demo-api",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use(handle);
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), demoApi()],
  server: {
    host: "127.0.0.1",
    proxy: {
      "/api": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: true,
  },
});
