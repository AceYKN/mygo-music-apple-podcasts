import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
} from 'node:fs';
import { join, resolve } from 'node:path';
if (process.platform !== 'win32' || !process.env.APPDATA)
  throw Error('This development installer currently supports Windows only');
const destination = join(
  process.env.APPDATA,
  'MyGo Music',
  'plugins',
  'apple-podcasts',
);
if (
  existsSync(join(destination, 'plugin.json')) &&
  JSON.parse(readFileSync(join(destination, 'plugin.json'), 'utf8')).id !==
    'apple-podcasts'
)
  throw Error('Refusing to replace a different plugin');
mkdirSync(join(destination, 'bin'), { recursive: true });
mkdirSync(join(destination, 'locales'), { recursive: true });
for (const name of [
  'index.js',
  'bin/apple-podcasts-helper.exe',
  ...readdirSync('locales')
    .filter((x) => x.endsWith('.json'))
    .map((x) => 'locales/' + x),
  'plugin.json',
])
  copyFileSync(resolve(name), join(destination, name));
console.log(
  'Installed Apple Podcasts development plugin. The private browser profile was preserved.',
);
