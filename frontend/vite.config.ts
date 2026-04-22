import path from "path";
import { fileURLToPath } from "url";
import { defineConfig, Plugin } from "vite";
import react from "@vitejs/plugin-react";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * Redirect `import ... from "/wails/runtime.js"` to our local typed wrapper.
 * The wrapper uses `import(/* @vite-ignore *\/ RUNTIME_URL)` so Vite never
 * sees the circular reference, and the browser fetches the real runtime
 * from the Go backend at run-time.
 */
function wailsRuntimePlugin(): Plugin {
  const localRuntime = path.resolve(__dirname, "src/wailsjs/runtime.ts");
  return {
    name: "vite-plugin-wails-runtime",
    enforce: "pre",
    resolveId(id) {
      if (id === "/wails/runtime.js") {
        return localRuntime;
      }
    },
  };
}

export default defineConfig({
  plugins: [wailsRuntimePlugin(), react()],
  server: {
    port: 34115,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      external: ["/wails/runtime.js"],
    },
  },
});
