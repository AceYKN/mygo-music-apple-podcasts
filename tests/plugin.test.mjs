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

test('Native playback returns audio and forwards actual progress without Chrome player commands', async () => {
  const plays = [];
  const p = await plugin(async (method, args) => ({
    ok: true,
    data:
      method === 'auth.status'
        ? session
        : method === 'api.get'
          ? { data: [episode('1001')] }
          : method === 'media.stream'
            ? { url: 'https://example.invalid/full.mp3', type: 'mp3' }
            : method === 'progress.start'
              ? { position: 120000, context: 'a' }
              : method === 'progress.record'
                ? (plays.push(args), { queued: true })
                : true,
  }));
  assert.equal((await p.stream({ id: '1001' })).type, 'mp3');
  assert.equal((await p.playbackStart({ id: '1001' })).position, 120000);
  const progress = {
    id: '1001',
    position: 30000,
    duration: 3600000,
    playing: false,
    completed: false,
    context: 'a',
    reason: 'state',
    recordedAt: '2026-11-10T00:00:00Z',
  };
  await p.playbackProgress(progress);
  assert.deepEqual(plays, [progress]);
  assert.equal(p.remotePlay, undefined);
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

test('Native streams resolve current permissions again after changing accounts', async () => {
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
    if (method === 'media.stream') {
      if (account === 'b')
        return {
          ok: false,
          error: { code: 'unavailable', message: 'No entitlement' },
        };
      played = true;
      return {
        ok: true,
        data: { url: 'https://example.invalid/full.mp3', type: 'mp3' },
      };
    }
    return { ok: true, data: true };
  });
  await p.search({ query: 'Example', kind: 'tracks' });
  account = 'b';
  await p.account();
  await assert.rejects(
    () => p.stream({ id: '1002' }),
    (e) => e.code === 'unavailable',
  );
  assert.equal(played, false);
});

test('Concurrent stream requests share one resolution and logout rejects its late result', async () => {
  let release, started;
  const gate = new Promise((resolve) => {
      release = resolve;
    }),
    entered = new Promise((resolve) => {
      started = resolve;
    });
  let requests = 0;
  const p = await plugin(async (method) => {
    if (method === 'media.stream') {
      requests++;
      started();
      await gate;
      return {
        ok: true,
        data: { url: 'https://example.invalid/private.mp3', type: 'mp3' },
      };
    }
    return { ok: true, data: true };
  });
  const a = p.stream({ id: '1001' }),
    b = p.stream({ id: '1001' });
  await entered;
  const settled = Promise.allSettled([a, b]);
  await p.signOut();
  release();
  const results = await settled;
  assert.equal(requests, 1);
  for (const result of results) {
    assert.equal(result.status, 'rejected');
    assert.equal(result.reason.code, 'conflict');
  }
});

test('Concurrent account checks share one request and reuse the confirmed session', async () => {
  let checks = 0;
  const p = await plugin(async (method) => {
    if (method === 'auth.status') checks++;
    return { ok: true, data: session };
  });
  const accounts = await Promise.all([p.account(), p.account()]);
  assert.equal(accounts[0].id, 'a');
  await p.account();
  assert.equal(checks, 1);
});

test('A region-filtered library is explained and My Page refresh clears a recovered route', async () => {
  let filtered = true,
    refreshes = 0;
  const p = await plugin(async (method, args) => {
    if (method === 'auth.status') {
      if (args.refresh) refreshes++;
      return { ok: true, data: session };
    }
    const following = args.path.includes('/library/podcasts?');
    return {
      ok: true,
      data: {
        data: following ? [{ id: '42', attributes: { name: 'Example' } }] : [],
        ...(filtered && following
          ? { sync: { stale: true, reason: 'region', routeCountry: 'cn' } }
          : {}),
      },
    };
  });
  const first = await p.home();
  assert.match(first.description, /another region/);
  assert.equal(first.sections[1].shows[0].id, '42');
  filtered = false;
  const recovered = await p.browse({ page: 'account' });
  assert.doesNotMatch(recovered.description ?? '', /another region/);
  assert.equal(refreshes, 2);
});

test('An empty episode page with an advancing cursor does not hide later episodes', async () => {
  const p = await plugin(async (method, args) => ({
    ok: true,
    data:
      method === 'auth.status'
        ? session
        : args.path === '/v1/empty'
          ? { data: [], next: '/v1/later' }
          : args.path === '/v1/later'
            ? { data: [episode('1001')] }
            : {
                data: [
                  {
                    id: '42',
                    relationships: {
                      episodes: { data: [], next: '/v1/empty' },
                    },
                  },
                ],
              },
  }));
  const result = await p.show({ id: '42' });
  assert.equal(result.episodes[0].id, '1001');
  assert.equal(result.more, false);
});

test('Following always reflects arbitrary cloud additions and removals and rejects filtered snapshots', async () => {
  let cloud = ['42'],
    filtered = false,
    checks = 0;
  const p = await plugin(async (method, args) => {
    if (method === 'auth.status') {
      if (args.refresh) checks++;
      return { ok: true, data: session };
    }
    return {
      ok: true,
      data: {
        data: cloud.map((id) => ({ id, attributes: { name: 'Show ' + id } })),
        ...(filtered
          ? { sync: { stale: true, reason: 'region', routeCountry: 'cn' } }
          : {}),
      },
    };
  });
  assert.deepEqual(
    (await p.shows()).map((x) => x.id),
    ['42'],
  );
  cloud = ['42', '777'];
  assert.deepEqual(
    (await p.shows()).map((x) => x.id),
    ['42', '777'],
  );
  cloud = ['777'];
  assert.deepEqual(
    (await p.shows()).map((x) => x.id),
    ['777'],
  );
  filtered = true;
  await assert.rejects(
    () => p.shows(),
    (e) => e.code === 'network',
  );
  assert.equal(checks, 4);
});
