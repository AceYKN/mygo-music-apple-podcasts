import { rpc, PluginError, disconnect } from './bridge';
import {
  Resource,
  Result,
  Browse,
  toShow,
  toTrack,
  numericID,
  shareID,
} from './models';

interface Session {
  authorized: boolean;
  country: string;
  id: string;
  offline?: boolean;
  routeCountry?: string;
}
let current: Session | undefined;
let sessionCheckedAt = 0;
let sessionEpoch = 0;
let streamEpoch = 0;
let checkingSession: Promise<Session> | undefined;
const pendingStreams = new Map<
  string,
  Promise<{ url: string; type: string }>
>();
const pages = new Map<
  string,
  { episodes: Resource[]; next?: string; show: Resource }
>();
let staleAt: string | undefined;
let regionFiltered = false;
interface PlaybackProgress {
  id: string;
  position: number;
  duration: number;
  playing: boolean;
  completed: boolean;
  context: string;
  reason: string;
  recordedAt: string;
}
let nativeProgress: PlaybackProgress | undefined;
const resourceList = (items: Resource[] = []) => items;
async function session(required = false, force = false): Promise<Session> {
  if (!force && current && Date.now() - sessionCheckedAt < 45000) {
    if (required && !current.authorized)
      throw new PluginError(
        'signIn',
        host.t('Sign in to Apple Podcasts first'),
      );
    return current;
  }
  const generation = sessionEpoch;
  const pending = (checkingSession ??= rpc<Session>('auth.status', {
    refresh: force,
  }));
  let next: Session;
  try {
    next = await pending;
  } finally {
    if (checkingSession === pending) checkingSession = undefined;
  }
  if (generation !== sessionEpoch)
    throw new PluginError(
      'conflict',
      host.t('The Apple Podcasts account changed. Refresh before continuing.'),
    );
  if (current?.id !== next.id) {
    if (current) {
      streamEpoch++;
      pendingStreams.clear();
    }
    pages.clear();
    staleAt = undefined;
    regionFiltered = false;
    nativeProgress = undefined;
  }
  current = next;
  sessionCheckedAt = Date.now();
  if (required && !next.authorized)
    throw new PluginError('signIn', host.t('Sign in to Apple Podcasts first'));
  return next;
}
async function get(path: string, all = false): Promise<Result> {
  const result = await rpc<Result>('api.get', { path, all });
  if (result.sync?.stale) staleAt = result.sync.at;
  if (result.sync?.reason === 'region') regionFiltered = true;
  return result;
}
const syncDescription = () =>
  regionFiltered ||
  (current?.routeCountry && current.routeCountry !== current.country)
    ? host.t(
        'This network redirects Apple Podcasts to another region. Some followed shows may be hidden. Check proxy rules and refresh; missing rows do not mean you unfollowed them.',
      )
    : current?.offline || staleAt
      ? host.t(
          'Offline: showing the last confirmed snapshot. Changes require confirmation in Playback and Sync.',
        ) + (staleAt ? ` (${staleAt})` : '')
      : undefined;
const epQuery =
  'include[podcast-episodes]=podcast,playback-position&extend[podcast-episodes]=inLibrary&platform=web';
async function library(
  kind: 'podcasts' | 'podcast-episodes' | 'podcast-channels',
): Promise<Resource[]> {
  await session(true);
  return resourceList(
    (await get(`/v1/me/library/${kind}?${epQuery}&limit=60`, true)).data,
  );
}
async function continuing(): Promise<Resource[]> {
  await session(true);
  return resourceList(
    (
      await get(
        `/v1/me/continue-playing/podcast-episodes?${epQuery}&limit=30`,
        true,
      )
    ).data,
  );
}
async function episode(id: string): Promise<Resource> {
  numericID(id);
  const s = await session();
  const r = await get(
    `/v1/catalog/${s.country}/podcast-episodes/${id}?${epQuery}&with=entitlements`,
  );
  const item = r.data?.[0];
  if (r.sync?.stale)
    throw new PluginError(
      'network',
      host.t(
        'Reconnect before starting playback so the latest position and entitlement can be checked',
      ),
    );
  if (!item)
    throw new PluginError(
      'unavailable',
      host.t('This episode is not available'),
    );
  resourceList([item]);
  return item;
}
export async function account() {
  const s = await session();
  return s.authorized
    ? {
        id: s.id,
        name: `Apple Podcasts (${s.country.toUpperCase()})`,
        page: 'account',
      }
    : null;
}
export async function signIn() {
  host.signInPrompt({
    text: host.t('Sign in on the official Apple Podcasts page in Chrome'),
  });
  await rpc('auth.start');
  const deadline = Date.now() + 270000;
  while (Date.now() < deadline) {
    const s = await session(false, true);
    const a = s.authorized
      ? {
          id: s.id,
          name: `Apple Podcasts (${s.country.toUpperCase()})`,
          page: 'account',
        }
      : null;
    if (a) return a;
    await new Promise((resolve) => setTimeout(resolve, 1500));
  }
  throw new PluginError(
    'signIn',
    host.t('Signing in took too long. Try again.'),
  );
}
export async function signOut() {
  streamEpoch++;
  sessionEpoch++;
  sessionCheckedAt = 0;
  checkingSession = undefined;
  pendingStreams.clear();
  await rpc('auth.signout');
  current = undefined;
  pages.clear();
}
export async function setSetting() {
  streamEpoch++;
  sessionEpoch++;
  sessionCheckedAt = 0;
  checkingSession = undefined;
  pendingStreams.clear();
  await disconnect();
  current = undefined;
  pages.clear();
}
export async function shows() {
  await session(true, true);
  const result = await get(`/v1/me/library/podcasts?${epQuery}&limit=60`, true);
  if (result.sync?.reason === 'region' || result.sync?.stale)
    throw new PluginError(
      'network',
      syncDescription() ??
        host.t('Cannot reach Apple Podcasts. Check your network and retry.'),
    );
  return resourceList(result.data).map((item) => toShow(item, true));
}
export async function liked({ offset = 0 }: { offset?: number } = {}) {
  const all = await library('podcast-episodes');
  return {
    tracks: all.slice(offset, offset + 30).map(toTrack),
    more: offset + 30 < all.length,
  };
}
export async function isLiked({ ids }: { ids: string[] }) {
  const saved = new Set((await library('podcast-episodes')).map((x) => x.id));
  return Object.fromEntries(ids.map((id) => [id, saved.has(id)]));
}
export async function recent() {
  return (await continuing()).map(toTrack);
}

export async function show({
  id,
  offset = 0,
}: {
  id: string;
  offset?: number;
}) {
  numericID(id);
  const s = await session(),
    key = `${s.id}:${s.country}:${id}`;
  let page = pages.get(key);
  if (offset === 0 || !page) {
    const data = await get(
      `/v1/catalog/${s.country}/podcasts/${id}?include=episodes&${epQuery}&limit[episodes]=30&with=entitlements`,
    );
    const detail = data.data?.[0];
    if (!detail)
      throw new PluginError(
        'unavailable',
        host.t('This show is not available'),
      );
    const list = detail.relationships?.episodes;
    page = {
      show: detail,
      episodes: resourceList(list?.data ?? []),
      next: list?.next,
    };
    pages.set(key, page);
  }
  const cursors = new Set<string>();
  while (page.episodes.length <= offset && page.next) {
    if (cursors.size >= 200)
      throw new PluginError(
        'network',
        host.t('Apple Podcasts library exceeds the pagination limit'),
      );
    if (cursors.has(page.next))
      throw new PluginError(
        'network',
        host.t('Apple Podcasts repeated a pagination cursor'),
      );
    cursors.add(page.next);
    const response = await get(page.next);
    const known = new Set(page.episodes.map((item) => item.id));
    page.episodes.push(
      ...resourceList(
        (response.data ?? []).filter((item) => !known.has(item.id)),
      ),
    );
    page.next = response.next;
  }
  return {
    show: toShow(page.show, !!page.show.attributes?.inLibrary),
    episodes: page.episodes.slice(offset, offset + 30).map(toTrack),
    more: offset + 30 < page.episodes.length || !!page.next,
  };
}

export async function search({
  query,
  kind,
  offset = 0,
}: {
  query: string;
  kind: string;
  offset?: number;
}) {
  if (!['shows', 'tracks'].includes(kind)) return { [kind]: [], more: false };
  const link = shareID(query.trim());
  if (link) {
    if (offset) return { [kind]: [], more: false };
    if (link.kind === 'tracks')
      return { tracks: [toTrack(await episode(link.id))], more: false };
    return { shows: [(await show({ id: link.id })).show], more: false };
  }
  const s = await session();
  const type = kind === 'shows' ? 'podcasts' : 'podcast-episodes';
  const params = new URLSearchParams({
    term: query,
    types: type,
    limit: '25',
    offset: String(offset),
    platform: 'web',
  });
  const r = await get(`/v1/catalog/${s.country}/search?${params}&${epQuery}`);
  const result = r.results?.[type] ?? { data: r.data, next: r.next };
  const list = resourceList(result.data ?? []);
  return {
    [kind]:
      kind === 'shows'
        ? list.map((x) => toShow(x, !!x.attributes?.inLibrary))
        : list.map(toTrack),
    more: !!result.next,
  };
}
export async function subscribeShow({ id, on }: { id: string; on: boolean }) {
  await session(true);
  numericID(id);
  await rpc('library.set', { kind: 'podcasts', id, on });
  pages.clear();
}
export async function likeEpisode({ id, on }: { id: string; on: boolean }) {
  await session(true);
  numericID(id);
  await rpc('library.set', { kind: 'podcast-episodes', id, on });
}

export async function home() {
  staleAt = undefined;
  regionFiltered = false;
  const s = await session(false, true);
  if (!s.authorized)
    return {
      title: host.t('Apple Podcasts'),
      description: host.t(
        'Sign in to load your library, saved episodes and listening progress.',
      ),
      sections: [],
    };
  const [tracks, following] = await Promise.all([
    continuing(),
    library('podcasts'),
  ]);
  const followed = following.map((item) => toShow(item, true));
  return {
    title: host.t('Apple Podcasts'),
    description:
      syncDescription() ??
      (!host.capabilities?.libraryRefresh
        ? host.t(
            'This MyGo build cannot automatically refresh your library. Install the updated companion MyGo build.',
          )
        : !host.capabilities?.playbackProgress
          ? host.t(
              'This MyGo build cannot report playback progress. Use the companion MyGo build for resume and progress sync.',
            )
          : undefined),
    sections: [
      {
        title: host.t('Continue Listening'),
        tracks: tracks.map(toTrack),
        morePage: 'continue',
        empty: host.t('Nothing to continue'),
      },
      {
        title: host.t('Following'),
        shows: followed,
        empty: host.t('No followed shows'),
      },
    ],
  };
}
export async function browse({ page }: { page: string }): Promise<Browse> {
  if (page === 'continue')
    return {
      title: host.t('Continue Listening'),
      sections: [{ tracks: (await continuing()).map(toTrack) }],
    };
  if (page === 'latest') {
    await session(true);
    const r = await get(
      `/v1/me/library/latest-episodes?${epQuery}&limit=60`,
      true,
    );
    return {
      title: host.t('Latest Episodes'),
      sections: [{ tracks: resourceList(r.data).map(toTrack) }],
    };
  }
  if (page === 'account') return home();
  if (page === 'sync') {
    const s = await session(true);
    const sync = await rpc<{
      pending: {
        id: string;
        kind: string;
        item: string;
        on: boolean;
        created: string;
        error?: string;
      }[];
    }>('sync.status');
    const state =
      nativeProgress?.context === s.id
        ? { trackId: nativeProgress.id }
        : undefined;
    const links = state?.trackId
      ? [
          {
            title: host.t('Mark as Played'),
            page: `completed:${state.trackId}:1`,
            icon: 'check',
          },
          {
            title: host.t('Mark as Unplayed'),
            page: `completed:${state.trackId}:0`,
            icon: 'repeat',
          },
        ]
      : [];
    if (state?.trackId)
      links.unshift({
        title: host.t('Episode Listening Status'),
        page: `episode:${state.trackId}`,
        icon: 'podcast',
      });
    const pending = sync.pending.flatMap((op) => {
      const action =
        op.kind === 'position'
          ? 'Sync Playback Position'
          : op.kind === 'completed'
            ? op.on
              ? 'Mark as Played'
              : 'Mark as Unplayed'
            : op.kind === 'podcasts'
              ? op.on
                ? 'Follow'
                : 'Unfollow'
              : op.on
                ? 'Save Episode'
                : 'Unsave Episode';
      return [
        {
          title: `${host.t('Retry')}: ${host.t(action)} · ${op.item}`,
          subtitle: op.created,
          page: `retry:${op.id}`,
        },
        {
          title: `${host.t('Discard Pending Change')} · ${op.item}`,
          page: `discard:${op.id}`,
        },
      ];
    });
    return {
      title: host.t('Playback and Sync'),
      description: s.offline
        ? syncDescription()
        : host.t('Changes are confirmed against your Apple Podcasts account.'),
      sections: [
        {
          title: host.t('Unconfirmed Changes'),
          links: pending,
          empty: host.t('All changes are confirmed'),
        },
        {
          title: host.t('Current Episode'),
          links,
          empty: host.t('Play an episode to manage its completion state'),
        },
      ],
    };
  }
  const episodePage = /^episode:(\d+)$/.exec(page);
  if (episodePage) {
    const item = await episode(episodePage[1]),
      track = toTrack(item);
    const cloud =
      item.relationships?.['playback-position']?.data?.[0]?.attributes;
    const position = Number(cloud?.positionInMilliseconds) || 0;
    const status = host.t(
      cloud?.completed ? 'Played' : position > 0 ? 'In Progress' : 'Unplayed',
    );
    return {
      title: track.name,
      description: `${host.t('Listening Status')}: ${status}\n${host.t('Cloud Position')}: ${Math.floor(position / 1000)} ${host.t('seconds')}\n\n${track.description}`,
      sections: [
        { tracks: [track] },
        {
          links: [
            { title: host.t('Mark as Played'), page: `completed:${item.id}:1` },
            {
              title: host.t('Mark as Unplayed'),
              page: `completed:${item.id}:0`,
            },
          ],
        },
      ],
    };
  }
  const pendingAction = /^(retry|discard):([a-f0-9]{24})$/.exec(page);
  if (pendingAction) {
    await session(true);
    await rpc(`sync.${pendingAction[1]}`, { operation: pendingAction[2] });
    return browse({ page: 'sync' });
  }
  const complete = /^completed:(\d+):([01])$/.exec(page);
  if (complete) {
    await session(true);
    await rpc('completion.set', { id: complete[1], on: complete[2] === '1' });
    return {
      title: host.t('Playback and Sync'),
      description: host.t('Synced with Apple Podcasts'),
      sections: [],
    };
  }
  throw new PluginError('unsupported', host.t('This page is not available'));
}

// MyGo owns decoding, playback, seeking and volume. The helper only supplies
// media metadata and persists/sends account synchronization requests.
export async function stream({ id }: { id: string }) {
  numericID(id);
  const generation = streamEpoch,
    key = `${generation}:${id}`;
  if (pendingStreams.has(key)) return pendingStreams.get(key)!;
  const pending = rpc<{ url: string; type: string }>('media.stream', {
    id,
  }).then((result) => {
    if (generation !== streamEpoch)
      throw new PluginError(
        'conflict',
        host.t(
          'The Apple Podcasts account changed. Refresh before continuing.',
        ),
      );
    return result;
  });
  pendingStreams.set(key, pending);
  try {
    return await pending;
  } finally {
    if (pendingStreams.get(key) === pending) pendingStreams.delete(key);
  }
}
export async function playbackStart({ id }: { id: string }) {
  numericID(id);
  return rpc<{ position: number; context: string }>('progress.start', { id });
}
export async function playbackProgress(progress: PlaybackProgress) {
  if (!progress.context) return;
  nativeProgress = progress;
  await rpc('progress.record', progress as unknown as Record<string, unknown>);
  if (progress.reason === 'quit') await rpc('progress.flush');
}
