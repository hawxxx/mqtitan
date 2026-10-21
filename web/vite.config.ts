import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/api": "http://localhost:8080" } },
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            { name: "tanstack", test: /node_modules[\\/]@tanstack[\\/]/ },
            { name: "charts", test: /node_modules[\\/]uplot[\\/]/ },
          ],
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
