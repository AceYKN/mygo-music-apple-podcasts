import { build } from 'esbuild';
await build({
  entryPoints: ['src/index.ts'],
  outfile: 'index.js',
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  target: 'es2022',
  legalComments: 'none',
});
