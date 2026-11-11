import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { zipSync } from 'fflate';

// Package the exact companion source used by this preview alongside its binary.
const source = resolve(process.argv[2] ?? '../mygo-music');
const commit = '59c76a17aae40fb20e41bd6ac2e25c77c90f553a';
const head = execFileSync('git', ['rev-parse', 'HEAD'], {
  cwd: source,
  encoding: 'utf8',
}).trim();
const dirty = execFileSync('git', ['status', '--porcelain'], {
  cwd: source,
  encoding: 'utf8',
}).trim();
if (head !== commit || dirty)
  throw Error('Companion source must be clean and match the documented commit');
const build = join(source, 'build-apple-podcasts/windows-amd64');
const files = {};
for (const name of ['MyGo Music.exe', 'icon.png', '.mygo-files'])
  files[name] = readFileSync(join(build, name));
files['LICENSE'] = readFileSync(join(source, 'LICENSE'));
files['SOURCE.txt'] = new TextEncoder().encode(
  `MyGo Music 0.1.22 with optional native playback progress hooks\n` +
    `Source commit: ${commit}\n` +
    `License: AGPL-3.0 (see LICENSE)\n` +
    `Corresponding source: mygo-music-native-progress-source.zip in the same release\n` +
    `Patch, build instructions and source download:\nhttps://github.com/AceYKN/mygo-music-apple-podcasts/releases/tag/v0.2.0-preview.2\n` +
    `This is a companion build, not an official MyGo release.\n`,
);
files['使用说明.txt'] = new TextEncoder().encode(
  '此为苹果播客插件的配套 MyGo 预览版，增加原生续播与进度回调。\r\n' +
    '解压到独立目录，退出已有 MyGo 后运行 MyGo Music.exe。\r\n' +
    '沿用当前 Windows 用户的 MyGo 数据与已安装插件，无需删除或迁移登录数据。\r\n' +
    '苹果播客插件另安装同版本 apple-podcasts-windows-amd64.zip。\r\n' +
    '请保留现有正式版程序，便于回退。此包不是 MyGo 官方发行版。\r\n',
);
mkdirSync('dist', { recursive: true });
writeFileSync(
  'dist/MyGo-Music-native-progress-windows-amd64.zip',
  zipSync(files, { level: 6 }),
);
execFileSync(
  'git',
  [
    'archive',
    '--format=zip',
    `--output=${resolve('dist/mygo-music-native-progress-source.zip')}`,
    commit,
  ],
  { cwd: source },
);
console.log(
  'Packaged companion Windows app, AGPL license and complete corresponding source.',
);
