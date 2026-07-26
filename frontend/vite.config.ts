import path from "path";
import { fileURLToPath } from "url";
import { defineConfig, Plugin } from "vite";
import react from "@vitejs/plugin-react";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * Generated Wails bindings import the runtime from /wails/runtime.js.
 * Vite treats /public JavaScript files as static assets, so rewrite those
 * imports to the npm runtime package before import analysis runs.
 */
function wailsBindingsPlugin(): Plugin {
  return {
    name: "vite-plugin-wails-bindings",
    enforce: "pre",
    transform(code, id) {
      if (!/\.[cm]?[jt]sx?$/.test(id)) {
        return null;
      }

      let nextCode = code.replace(
        /((?:from|export\s+\*)\s+)(["'])\/wails(?:js)?\/runtime\.js\2/g,
        '$1"@wailsio/runtime"',
      );

      if (id.includes("/frontend/bindings/")) {
        nextCode = nextCode.replace(
          /((?:from|export\s+\*)\s+)(["'])(\.\.?\/[^"']+)\.js\2/g,
          '$1$2$3.ts$2',
        );
      }

      if (nextCode === code) {
        return null;
      }

      return {
        code: nextCode,
        map: null,
      };
    },
  };
}

export default defineConfig({
  plugins: [wailsBindingsPlugin(), react()],
  resolve: {
    alias: {
      "@bindings": path.resolve(__dirname, "bindings"),
    },
  },
  server: {
    port: 34115,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
