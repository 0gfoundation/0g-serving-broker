import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { crx, ManifestV3Export } from "@crxjs/vite-plugin";
import manifest from "./manifest.json";
import svgr from "vite-plugin-svgr";
import path from "path";

export default defineConfig({
  plugins: [
    svgr(),
    react(),
    crx({ manifest: manifest as unknown as ManifestV3Export }),
  ],
  resolve: {
    alias: {
      "@src": path.resolve(__dirname, "src"),
    },
  },
  server: {
    port: 3001,
    watch: {
      ignored: ["node_modules/**", "**/src/apis/models/**"],
    },
    hmr: {
      protocol: "ws",
      host: "localhost",
      port: 3001,
      overlay: false,
    },
  },
});
