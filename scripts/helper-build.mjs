import { mkdirSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
mkdirSync('bin', { recursive: true });
const name =
  process.platform === 'win32'
    ? 'apple-podcasts-helper.exe'
    : 'apple-podcasts-helper';
const result = spawnSync(
  'go',
  [
    'build',
    '-trimpath',
    '-ldflags=-s -w',
    '-o',
    `../bin/${name}`,
    './cmd/apple-podcasts-helper',
  ],
  {
    cwd: 'helper',
    stdio: 'inherit',
    env: { ...process.env, CGO_ENABLED: '0' },
  },
);
process.exitCode = result.status ?? 1;
