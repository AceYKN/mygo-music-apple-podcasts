export interface Resource {
  id: string;
  type: string;
  attributes?: Record<string, any>;
  relationships?: Record<string, { data?: Resource[]; next?: string }>;
  meta?: Record<string, any>;
}
export interface Result {
  data?: Resource[];
  next?: string;
  results?: Record<string, { data?: Resource[]; next?: string }>;
  included?: Resource[];
  sync?: { stale: boolean; at: string };
}
export interface Track {
  id: string;
  name: string;
  showId: string;
  artists: { id: string; name: string }[];
  album: { id: string; name: string; imageUrl?: string };
  duration: number;
  description: string;
  published: string;
  url?: string;
  unavailable?: boolean;
}
export interface Browse {
  title: string;
  description?: string;
  sections: {
    title?: string;
    tracks?: Track[];
    shows?: ReturnType<typeof toShow>[];
    links?: { title: string; subtitle?: string; page: string; icon?: string }[];
    empty?: string;
    morePage?: string;
  }[];
}

export function plain(value: unknown): string {
  const text =
    typeof value === 'object' && value !== null
      ? String(
          (value as Record<string, unknown>).standard ??
            (value as Record<string, unknown>).short ??
            '',
        )
      : String(value ?? '');
  return text
    .replace(/<\/(?:p|div)>|<br\s*\/?\s*>/gi, '\n')
    .replace(/<[^>]*>/g, '')
    .replace(
      /&(amp|lt|gt|quot|apos|nbsp);/g,
      (_, key: string) =>
        ({ amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ' })[
          key
        ] ?? '',
    )
    .replace(/&#(x[0-9a-f]+|\d+);/gi, (all, key: string) => {
      const n =
        key[0].toLowerCase() === 'x' ? parseInt(key.slice(1), 16) : Number(key);
      return n >= 0 && n <= 0x10ffff ? String.fromCodePoint(n) : all;
    })
    .trim();
}
export function artwork(value: any): string | undefined {
  if (!value?.url) return undefined;
  return String(value.url)
    .replaceAll('{w}', '600')
    .replaceAll('{h}', '600')
    .replaceAll('{f}', 'jpg')
    .replaceAll('{c}', 'bb');
}
export function toShow(r: Resource, subscribed = false) {
  const a = r.attributes ?? {};
  return {
    id: r.id,
    name: String(a.name ?? ''),
    publisher: String(a.artistName ?? ''),
    description: plain(a.description),
    imageUrl: artwork(a.artwork),
    episodeCount: Number(a.trackCount) || 0,
    subscribed,
  };
}
export function toTrack(r: Resource): Track {
  const a = r.attributes ?? {},
    show = r.relationships?.podcast?.data?.[0],
    sa = show?.attributes ?? {};
  const showId = show?.id ?? String(a.podcastId ?? '');
  return {
    id: r.id,
    name: String(a.name ?? ''),
    showId,
    artists: [{ id: '', name: String(sa.artistName ?? a.artistName ?? '') }],
    album: {
      id: showId,
      name: String(sa.name ?? a.artistName ?? ''),
      imageUrl: artwork(a.artwork ?? sa.artwork),
    },
    duration: Number(a.durationInMilliseconds ?? a.durationInMillis) || 0,
    description: plain(a.description),
    published: String(a.releaseDateTime ?? a.releaseDate ?? '').slice(0, 10),
    url: a.url,
    unavailable: a.isPlayable === false,
  };
}
export function playerItem(r: Resource): Resource {
  const a = r.attributes ?? {};
  return {
    ...r,
    type: 'podcast-episodes',
    attributes: {
      ...a,
      durationInMillis:
        Number(a.durationInMilliseconds ?? a.durationInMillis) || 0,
    },
  };
}
export function numericID(value: string): string {
  if (!/^\d{1,24}$/.test(value))
    throw new Error('Invalid Apple Podcasts item ID');
  return value;
}
export function shareID(
  query: string,
): { kind: 'shows' | 'tracks'; id: string } | null {
  try {
    const u = new URL(query);
    if (u.protocol !== 'https:' || u.hostname !== 'podcasts.apple.com')
      return null;
    const episode = u.searchParams.get('i');
    if (episode) return { kind: 'tracks', id: numericID(episode) };
    const id = /\/id(\d+)(?:\/|$)/.exec(u.pathname)?.[1];
    return id ? { kind: 'shows', id: numericID(id) } : null;
  } catch {
    return null;
  }
}
