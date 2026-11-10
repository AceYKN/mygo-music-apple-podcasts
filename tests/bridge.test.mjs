import test from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
const output = await build({
  entryPoints: ['src/bridge.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  write: false,
});
let serial = 0;
async function bridge() {
  return import(
    'data:text/javascript;base64,' +
      Buffer.from(
        output.outputFiles[0].text + `\n// test instance ${serial++}`,
      ).toString('base64')
  );
}
test('Helper starts once and reuses persistent private data', async () => {
  let starts = 0,
    args,
    headers;
  globalThis.host = {
    dir: 'test-plugin',
    dataDir: 'test-private-data',
    platform: 'windows',
    t: (x) => x,
    setting: () => undefined,
    spawn: (_file, a) => {
      starts++;
      args = a;
      return {
        readLine: async () =>
          JSON.stringify({
            protocol: 1,
            url: 'http://127.0.0.1:34567',
            token: 'b'.repeat(64),
          }),
        wait: () => new Promise(() => {}),
        kill() {},
      };
    },
  };
  const previous = globalThis.fetch;
  globalThis.fetch = async (_url, options) => {
    headers = options.headers;
    return new Response(JSON.stringify({ ok: true, data: { ready: true } }));
  };
  try {
    const b = await bridge();
    await Promise.all([b.rpc('health'), b.rpc('health')]);
    assert.equal(starts, 1);
    assert.deepEqual(args, ['--data-dir', 'test-private-data']);
    assert.equal(headers.authorization, 'Bearer ' + 'b'.repeat(64));
  } finally {
    globalThis.fetch = previous;
  }
});
test('Unexpected helper host is rejected before credentials can be sent', async () => {
  let killed = false;
  globalThis.host = {
    dir: 'test',
    dataDir: 'private',
    platform: 'windows',
    t: (x) => x,
    setting: () => undefined,
    spawn: () => ({
      readLine: async () =>
        JSON.stringify({
          protocol: 1,
          url: 'https://evil.invalid',
          token: 'b'.repeat(64),
        }),
      wait: () => new Promise(() => {}),
      kill() {
        killed = true;
      },
    }),
  };
  const b = await bridge();
  await assert.rejects(
    () => b.rpc('health'),
    (e) => e.code === 'setup',
  );
  assert.equal(killed, true);
});
