import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('.', import.meta.url));
const publics = fileURLToPath(new URL('../publics', import.meta.url));

export default defineConfig({
  plugins: [preact()],
  // Relative base so the embedded file server can serve assets from "/".
  base: './',
  // Reuse the repository's publics/ folder (foreman.png) instead of duplicating it.
  publicDir: publics,
  build: {
    outDir: `${root}../internal/foreman/web/dist`,
    emptyOutDir: true,
    target: 'es2020',
    sourcemap: false,
  },
});