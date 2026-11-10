import test from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
const output = await build({
  entryPoints: ['src/index.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  write: false,
});
let serial = 0;
async function plugin(handle) {
  globalThis.host = {
    dir: 'plugin',
    dataDir: 'private',
    platform: 'windows',
    t: (x) => x,
    setting: () => undefined,
    spawn: () => ({
      readLine: async () =>
        JSON.stringify({
          protocol: 1,
          url: 'http://127.0.0.1:12345',
          token: 'a'.repeat(64),
        }),
      wait: () => new Promise(() => {}),
      kill() {},
    }),
  };
  globalThis.fetch = async (_url, options) => {
    const { method, params } = JSON.parse(options.body);
    return new Response(JSON.stringify(await handle(method, params)));
  };
  return import(
    'data:text/javascript;base64,' +
      Buffer.from(
        output.outputFiles[0].text + `\n// instance ${serial++}`,
      ).toString('base64')
  );
}
const session = { authorized: true, country: 'us', id: 'a' };
const episode = (id, position = 120000) => ({
  id,
  type: 'podcast-episodes',
  attributes: { name: 'Example', durationInMilliseconds: 3600000 },
  relationships: {
    'playback-position': {
      data: [
        { attributes: { completed: false, positionInMilliseconds: position } },
      ],
    },
  },
});

test('Playback resumes from cloud and accepts an explicit backward position', async () => {
  const plays = [];
  const p = await plugin(async (method, args) => ({
    ok: true,
    data:
      method === 'auth.status'
        ? session
        : method === 'api.get'
          ? { data: [episode('1001')] }
          : method === 'player.play'
            ? (plays.push(args), true)
            : true,
  }));
  await p.remotePlay({ ids: ['1001'] });
  await p.remotePlay({ ids: ['1001'], position: 30000 });
  assert.deepEqual(
    plays.map((x) => x.position),
    [120000, 30000],
  );
  assert.equal(plays[0].items[0].attributes.durationInMillis, 3600000);
  await p.browse({ page: 'beginning:1001' });
  assert.equal(plays.at(-1).position, 0);
});

test('Show pagination does not duplicate episodes at page boundaries', async () => {
  const first = Array.from({ length: 30 }, (_, i) => episode(String(1000 + i)));
  const second = [
    first.at(-1),
    ...Array.from({ length: 30 }, (_, i) => episode(String(1030 + i))),
  ];
  const p = await plugin(async (method, args) => ({
    ok: true,
    data:
      method === 'auth.status'
        ? session
        : args.path === '/v1/next'
          ? { data: second }
          : {
              data: [
                {
                  id: '42',
                  attributes: { name: 'Show' },
                  relationships: {
                    episodes: { data: first, next: '/v1/next' },
                  },
                },
              ],
            },
  }));
  const a = await p.show({ id: '42' }),
    b = await p.show({ id: '42', offset: 30 });
  assert.equal(a.more, true);
  assert.equal(b.more, false);
  assert.equal(b.episodes.length, 30);
  assert.equal(
    new Set([...a.episodes, ...b.episodes].map((x) => x.id)).size,
    60,
  );
});

test('Changing accounts invalidates cached episode permissions', async () => {
  let account = 'a',
    played = false;
  const p = await plugin(async (method, args) => {
    if (method === 'auth.status')
      return { ok: true, data: { ...session, id: account } };
    if (method === 'api.get') {
      if (args.path.includes('/search?'))
        return {
          ok: true,
          data: {
            results: {
              'podcast-episodes': { data: [episode('1001'), episode('1002')] },
            },
          },
        };
      if (account === 'b' && args.path.includes('/1002?'))
        return {
          ok: false,
          error: { code: 'unavailable', message: 'No entitlement' },
        };
      return { ok: true, data: { data: [episode('1001')] } };
    }
    if (method === 'player.play') played = true;
    return { ok: true, data: true };
  });
  await p.search({ query: 'Example', kind: 'tracks' });
  account = 'b';
  await p.account();
  await assert.rejects(
    () => p.remotePlay({ ids: ['1001', '1002'] }),
    (e) => e.code === 'unavailable',
  );
  assert.equal(played, false);
});
