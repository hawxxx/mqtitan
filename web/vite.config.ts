import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/api": "http://localhost:8080" } },
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          tanstack: [
            "@tanstack/react-router",
            "@tanstack/react-query",
            "@tanstack/react-table",
            "@tanstack/react-virtual",
          ],
          charts: ["uplot"],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    maxWorkers: 2,
    setupFiles: ["./src/test-setup.ts"],
  },
});
