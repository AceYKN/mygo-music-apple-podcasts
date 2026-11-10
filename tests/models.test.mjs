import test from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
const result = await build({
  entryPoints: ['src/models.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  write: false,
});
const model = await import(
  'data:text/javascript;base64,' +
    Buffer.from(result.outputFiles[0].text).toString('base64')
);

test('Apple sharing links reject lookalike domains and preserve episode identity', () => {
  assert.deepEqual(
    model.shareID('https://podcasts.apple.com/jp/podcast/test/id42?i=1001'),
    { kind: 'tracks', id: '1001' },
  );
  assert.deepEqual(
    model.shareID('https://podcasts.apple.com/us/podcast/test/id42'),
    { kind: 'shows', id: '42' },
  );
  assert.equal(
    model.shareID('https://podcasts.apple.com.evil.invalid/id42?i=1001'),
    null,
  );
  assert.equal(
    model.shareID('https://podcasts.apple.com/podcast/id42?i=../evil'),
    null,
  );
});
test('Episode mapping preserves milliseconds, podcast identity and show notes', () => {
  const r = {
    id: '1001',
    type: 'podcast-episodes',
    attributes: {
      name: 'Episode',
      durationInMilliseconds: 123456,
      description: { standard: '<p>A &amp; B</p><p>第二段</p>' },
      releaseDateTime: '2026-11-10T00:00:00Z',
      artwork: { url: 'https://example.invalid/{w}x{h}.{f}' },
    },
    relationships: {
      podcast: {
        data: [{ id: '42', attributes: { name: 'Show', artistName: 'Host' } }],
      },
    },
  };
  const t = model.toTrack(r);
  assert.equal(t.duration, 123456);
  assert.equal(t.showId, '42');
  assert.equal(t.album.name, 'Show');
  assert.equal(t.description, 'A & B\n第二段');
  assert.equal(t.published, '2026-11-10');
  assert.equal(t.album.imageUrl, 'https://example.invalid/600x600.jpg');
});
test('Paid entitlement is not inferred from public URL absence', () => {
  const r = {
    id: '1',
    type: 'podcast-episodes',
    attributes: {
      name: 'Subscriber episode',
      offers: [
        { type: 'PSUB', hlsUrl: 'https://example.invalid/authorized.m3u8' },
      ],
    },
  };
  assert.equal(model.toTrack(r).unavailable, false);
});
