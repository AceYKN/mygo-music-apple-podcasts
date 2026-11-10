import {
  mkdirSync,
  readFileSync,
  writeFileSync,
  readdirSync,
  statSync,
} from 'node:fs';
import { join } from 'node:path';
import { zipSync } from 'fflate';
const platform = { win32: 'windows', darwin: 'darwin', linux: 'linux' }[
  process.platform
];
const arch = { x64: 'amd64', arm64: 'arm64' }[process.arch];
if (!platform || !arch) throw Error('Unsupported packaging platform');
const name =
  platform === 'windows'
    ? 'apple-podcasts-helper.exe'
    : 'apple-podcasts-helper';
const files = {
  'plugin.json': readFileSync('plugin.json'),
  'index.js': readFileSync('index.js'),
  [`bin/${name}`]: [readFileSync(`bin/${name}`), { os: 3, attrs: 0o755 << 16 }],
};
for (const file of readdirSync('locales'))
  if (file.endsWith('.json'))
    files[`locales/${file}`] = readFileSync(join('locales', file));
mkdirSync('dist', { recursive: true });
const target = `dist/apple-podcasts-${platform}-${arch}.zip`;
writeFileSync(target, zipSync(files, { level: 6 }));
console.log(`${target} (${statSync(target).size} bytes)`);
