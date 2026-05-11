import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.indexOf("node_modules/react") >= 0 || id.indexOf("node_modules/react-dom") >= 0) {
            return "react";
          }
          if (id.indexOf("node_modules/antd") >= 0 || id.indexOf("node_modules/@ant-design") >= 0) {
            return "antd";
          }
          if (id.indexOf("node_modules") >= 0) {
            return "vendor";
          }
          return undefined;
        }
      }
    }
  },
  server: {
    port: 5173,
    strictPort: false
  },
  preview: {
    port: 4173
  }
});
