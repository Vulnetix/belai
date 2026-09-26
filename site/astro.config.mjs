// @ts-check
import { defineConfig } from 'astro/config';
import react from '@astrojs/react';
import tailwindcss from '@tailwindcss/vite';

// The custom domain (belai.vulnetix.com) is bound by public/CNAME, so Pages
// serves this project at the domain root. base must be '/': a '/belai/'
// base would emit asset URLs under a path the apex never exposes. Keep base
// in sync with public/CNAME.
export default defineConfig({
  site: 'https://belai.vulnetix.com',
  base: '/',
  trailingSlash: 'always',
  integrations: [react()],
  vite: {
    plugins: [tailwindcss()],
  },
});