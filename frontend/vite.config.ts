import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../web/dist',
    emptyOutDir: true,
    sourcemap: false,
	rollupOptions: {
	  // Semantic chunk names such as *Connection* are occasionally blocked by
	  // enterprise browser filters. Content hashes are stable and opaque.
	  output: {
	    chunkFileNames: 'assets/chunk-[hash].js',
	    // The platform is updated much more often than Vue and Arco Design.
	    // Keeping framework code in stable hashed chunks avoids making every
	    // operator download the full UI runtime after a backend-only rollout.
	    manualChunks(id) {
	      if (!id.includes('/node_modules/')) return undefined;
	      if (id.includes('/@arco-design/')) return 'ui-runtime';
	      if (id.includes('/vue/') || id.includes('/vue-router/') || id.includes('/pinia/')) return 'vue-runtime';
	      if (id.includes('/yaml/')) return 'yaml-runtime';
	      return 'vendor-runtime';
	    },
	  },
	},
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
});
