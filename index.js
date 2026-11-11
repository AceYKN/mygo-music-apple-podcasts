// src/bridge.ts
var PluginError = class extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
  code;
};
var pending;
var timeout = (promise, ms) => new Promise((resolve, reject) => {
  const id = setTimeout(
    () => reject(
      new PluginError(
        "setup",
        host.t("The Apple Podcasts helper did not respond")
      )
    ),
    ms
  );
  promise.then(
    (value) => {
      clearTimeout(id);
      resolve(value);
    },
    (error) => {
      clearTimeout(id);
      reject(error);
    }
  );
});
async function connect() {
  if (pending) return pending;
  pending = (async () => {
    const executable = host.platform === "windows" ? "apple-podcasts-helper.exe" : "apple-podcasts-helper";
    const args = ["--data-dir", host.dataDir];
    const chrome = host.setting("chromePath")?.trim();
    if (chrome) args.push("--chrome", chrome);
    let process;
    try {
      process = host.spawn(`${host.dir}/bin/${executable}`, args);
    } catch {
      throw new PluginError(
        "setup",
        host.t("Allow Apple Podcasts to start its helper in plugin settings")
      );
    }
    try {
      const line = await timeout(process.readLine(), 15e3);
      if (!line) throw new Error("No handshake");
      const data = JSON.parse(line);
      if (data.protocol !== 1 || !/^http:\/\/127\.0\.0\.1:\d{1,5}$/.test(data.url) || !/^[0-9a-f]{64}$/.test(data.token))
        throw new Error("Invalid handshake");
      const port = Number(data.url.split(":").at(-1));
      if (port < 1 || port > 65535) throw new Error("Invalid port");
      const connection = { ...data, process };
      const active = pending;
      void process.wait().then(
        () => {
          if (pending === active) pending = void 0;
        },
        () => {
          if (pending === active) pending = void 0;
        }
      );
      return connection;
    } catch {
      process.kill();
      throw new PluginError(
        "setup",
        host.t(
          "The Apple Podcasts helper could not start. Reinstall the plugin and check Chrome."
        )
      );
    }
  })();
  try {
    return await pending;
  } catch (error) {
    pending = void 0;
    throw error;
  }
}
async function disconnect() {
  const existing = pending;
  if (!existing) return;
  try {
    const c = await existing;
    c.process.closeStdin();
    try {
      await timeout(c.process.wait(), 2e4);
    } catch {
      c.process.kill();
    }
  } finally {
    if (pending === existing) pending = void 0;
  }
}
async function rpc(method, params = {}) {
  const connection = await connect();
  let response;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 175e3);
  try {
    response = await fetch(`${connection.url}/rpc`, {
      method: "POST",
      headers: {
        authorization: `Bearer ${connection.token}`,
        "content-type": "application/json"
      },
      body: JSON.stringify({ method, params }),
      signal: controller.signal
    });
  } catch {
    throw new PluginError(
      "network",
      host.t(
        "Apple Podcasts connection failed. Check the official window and retry."
      )
    );
  } finally {
    clearTimeout(timer);
  }
  if (!response.ok)
    throw new PluginError(
      "setup",
      host.t("The Apple Podcasts helper rejected the connection")
    );
  const result = await response.json();
  if (!result.ok)
    throw new PluginError(
      result.error?.code ?? "unavailable",
      host.t(
        result.error?.message ?? "Apple Podcasts could not complete the operation"
      ) + (result.error?.pending ? " " + host.t(
        "The change is unconfirmed. Open Playback and Sync to retry or discard it."
      ) : "")
    );
  return result.data;
}

// src/models.ts
function plain(value) {
  const text = typeof value === "object" && value !== null ? String(
    value.standard ?? value.short ?? ""
  ) : String(value ?? "");
  return text.replace(/<\/(?:p|div)>|<br\s*\/?\s*>/gi, "\n").replace(/<[^>]*>/g, "").replace(
    /&(amp|lt|gt|quot|apos|nbsp);/g,
    (_, key) => ({ amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " })[key] ?? ""
  ).replace(/&#(x[0-9a-f]+|\d+);/gi, (all, key) => {
    const n = key[0].toLowerCase() === "x" ? parseInt(key.slice(1), 16) : Number(key);
    return n >= 0 && n <= 1114111 ? String.fromCodePoint(n) : all;
  }).trim();
}
function artwork(value) {
  if (!value?.url) return void 0;
  return String(value.url).replaceAll("{w}", "600").replaceAll("{h}", "600").replaceAll("{f}", "jpg").replaceAll("{c}", "bb");
}
function toShow(r, subscribed = false) {
  const a = r.attributes ?? {};
  return {
    id: r.id,
    name: String(a.name ?? ""),
    publisher: String(a.artistName ?? ""),
    description: plain(a.description),
    imageUrl: artwork(a.artwork),
    episodeCount: Number(a.trackCount) || 0,
    subscribed
  };
}
function toTrack(r) {
  const a = r.attributes ?? {}, show2 = r.relationships?.podcast?.data?.[0], sa = show2?.attributes ?? {};
  const showId = show2?.id ?? String(a.podcastId ?? "");
  return {
    id: r.id,
    name: String(a.name ?? ""),
    showId,
    artists: [{ id: "", name: String(sa.artistName ?? a.artistName ?? "") }],
    album: {
      id: showId,
      name: String(sa.name ?? a.artistName ?? ""),
      imageUrl: artwork(a.artwork ?? sa.artwork)
    },
    duration: Number(a.durationInMilliseconds ?? a.durationInMillis) || 0,
    description: plain(a.description),
    published: String(a.releaseDateTime ?? a.releaseDate ?? "").slice(0, 10),
    url: a.url,
    unavailable: a.isPlayable === false
  };
}
function numericID(value) {
  if (!/^\d{1,24}$/.test(value))
    throw new Error("Invalid Apple Podcasts item ID");
  return value;
}
function shareID(query) {
  try {
    const u = new URL(query);
    if (u.protocol !== "https:" || u.hostname !== "podcasts.apple.com")
      return null;
    const episode2 = u.searchParams.get("i");
    if (episode2) return { kind: "tracks", id: numericID(episode2) };
    const id = /\/id(\d+)(?:\/|$)/.exec(u.pathname)?.[1];
    return id ? { kind: "shows", id: numericID(id) } : null;
  } catch {
    return null;
  }
}

// src/index.ts
var current;
var sessionCheckedAt = 0;
var sessionEpoch = 0;
var streamEpoch = 0;
var checkingSession;
var pendingStreams = /* @__PURE__ */ new Map();
var pages = /* @__PURE__ */ new Map();
var staleAt;
var regionFiltered = false;
var nativeProgress;
var resourceList = (items = []) => items;
async function session(required = false, force = false) {
  if (!force && current && Date.now() - sessionCheckedAt < 45e3) {
    if (required && !current.authorized)
      throw new PluginError(
        "signIn",
        host.t("Sign in to Apple Podcasts first")
      );
    return current;
  }
  const generation = sessionEpoch;
  const pending2 = checkingSession ??= rpc("auth.status", {
    refresh: force
  });
  let next;
  try {
    next = await pending2;
  } finally {
    if (checkingSession === pending2) checkingSession = void 0;
  }
  if (generation !== sessionEpoch)
    throw new PluginError(
      "conflict",
      host.t("The Apple Podcasts account changed. Refresh before continuing.")
    );
  if (current?.id !== next.id) {
    if (current) {
      streamEpoch++;
      pendingStreams.clear();
    }
    pages.clear();
    staleAt = void 0;
    regionFiltered = false;
    nativeProgress = void 0;
  }
  current = next;
  sessionCheckedAt = Date.now();
  if (required && !next.authorized)
    throw new PluginError("signIn", host.t("Sign in to Apple Podcasts first"));
  return next;
}
async function get(path, all = false) {
  const result = await rpc("api.get", { path, all });
  if (result.sync?.stale) staleAt = result.sync.at;
  if (result.sync?.reason === "region") regionFiltered = true;
  return result;
}
var syncDescription = () => regionFiltered || current?.routeCountry && current.routeCountry !== current.country ? host.t(
  "This network redirects Apple Podcasts to another region. Some followed shows may be hidden. Check proxy rules and refresh; missing rows do not mean you unfollowed them."
) : current?.offline || staleAt ? host.t(
  "Offline: showing the last confirmed snapshot. Changes require confirmation in Playback and Sync."
) + (staleAt ? ` (${staleAt})` : "") : void 0;
var epQuery = "include[podcast-episodes]=podcast,playback-position&extend[podcast-episodes]=inLibrary&platform=web";
async function library(kind) {
  await session(true);
  return resourceList(
    (await get(`/v1/me/library/${kind}?${epQuery}&limit=60`, true)).data
  );
}
async function continuing() {
  await session(true);
  return resourceList(
    (await get(
      `/v1/me/continue-playing/podcast-episodes?${epQuery}&limit=30`,
      true
    )).data
  );
}
async function episode(id) {
  numericID(id);
  const s = await session();
  const r = await get(
    `/v1/catalog/${s.country}/podcast-episodes/${id}?${epQuery}&with=entitlements`
  );
  const item = r.data?.[0];
  if (r.sync?.stale)
    throw new PluginError(
      "network",
      host.t(
        "Reconnect before starting playback so the latest position and entitlement can be checked"
      )
    );
  if (!item)
    throw new PluginError(
      "unavailable",
      host.t("This episode is not available")
    );
  resourceList([item]);
  return item;
}
async function account() {
  const s = await session();
  return s.authorized ? {
    id: s.id,
    name: `Apple Podcasts (${s.country.toUpperCase()})`,
    page: "account"
  } : null;
}
async function signIn() {
  host.signInPrompt({
    text: host.t("Sign in on the official Apple Podcasts page in Chrome")
  });
  await rpc("auth.start");
  const deadline = Date.now() + 27e4;
  while (Date.now() < deadline) {
    const s = await session(false, true);
    const a = s.authorized ? {
      id: s.id,
      name: `Apple Podcasts (${s.country.toUpperCase()})`,
      page: "account"
    } : null;
    if (a) return a;
    await new Promise((resolve) => setTimeout(resolve, 1500));
  }
  throw new PluginError(
    "signIn",
    host.t("Signing in took too long. Try again.")
  );
}
async function signOut() {
  streamEpoch++;
  sessionEpoch++;
  sessionCheckedAt = 0;
  checkingSession = void 0;
  pendingStreams.clear();
  await rpc("auth.signout");
  current = void 0;
  pages.clear();
}
async function setSetting() {
  streamEpoch++;
  sessionEpoch++;
  sessionCheckedAt = 0;
  checkingSession = void 0;
  pendingStreams.clear();
  await disconnect();
  current = void 0;
  pages.clear();
}
async function shows() {
  await session(true, true);
  const result = await get(`/v1/me/library/podcasts?${epQuery}&limit=60`, true);
  if (result.sync?.reason === "region" || result.sync?.stale)
    throw new PluginError(
      "network",
      syncDescription() ?? host.t("Cannot reach Apple Podcasts. Check your network and retry.")
    );
  return resourceList(result.data).map((item) => toShow(item, true));
}
async function liked({ offset = 0 } = {}) {
  const all = await library("podcast-episodes");
  return {
    tracks: all.slice(offset, offset + 30).map(toTrack),
    more: offset + 30 < all.length
  };
}
async function isLiked({ ids }) {
  const saved = new Set((await library("podcast-episodes")).map((x) => x.id));
  return Object.fromEntries(ids.map((id) => [id, saved.has(id)]));
}
async function recent() {
  return (await continuing()).map(toTrack);
}
async function show({
  id,
  offset = 0
}) {
  numericID(id);
  const s = await session(), key = `${s.id}:${s.country}:${id}`;
  let page = pages.get(key);
  if (offset === 0 || !page) {
    const data = await get(
      `/v1/catalog/${s.country}/podcasts/${id}?include=episodes&${epQuery}&limit[episodes]=30&with=entitlements`
    );
    const detail = data.data?.[0];
    if (!detail)
      throw new PluginError(
        "unavailable",
        host.t("This show is not available")
      );
    const list = detail.relationships?.episodes;
    page = {
      show: detail,
      episodes: resourceList(list?.data ?? []),
      next: list?.next
    };
    pages.set(key, page);
  }
  const cursors = /* @__PURE__ */ new Set();
  while (page.episodes.length <= offset && page.next) {
    if (cursors.size >= 200)
      throw new PluginError(
        "network",
        host.t("Apple Podcasts library exceeds the pagination limit")
      );
    if (cursors.has(page.next))
      throw new PluginError(
        "network",
        host.t("Apple Podcasts repeated a pagination cursor")
      );
    cursors.add(page.next);
    const response = await get(page.next);
    const known = new Set(page.episodes.map((item) => item.id));
    page.episodes.push(
      ...resourceList(
        (response.data ?? []).filter((item) => !known.has(item.id))
      )
    );
    page.next = response.next;
  }
  return {
    show: toShow(page.show, !!page.show.attributes?.inLibrary),
    episodes: page.episodes.slice(offset, offset + 30).map(toTrack),
    more: offset + 30 < page.episodes.length || !!page.next
  };
}
async function search({
  query,
  kind,
  offset = 0
}) {
  if (!["shows", "tracks"].includes(kind)) return { [kind]: [], more: false };
  const link = shareID(query.trim());
  if (link) {
    if (offset) return { [kind]: [], more: false };
    if (link.kind === "tracks")
      return { tracks: [toTrack(await episode(link.id))], more: false };
    return { shows: [(await show({ id: link.id })).show], more: false };
  }
  const s = await session();
  const type = kind === "shows" ? "podcasts" : "podcast-episodes";
  const params = new URLSearchParams({
    term: query,
    types: type,
    limit: "25",
    offset: String(offset),
    platform: "web"
  });
  const r = await get(`/v1/catalog/${s.country}/search?${params}&${epQuery}`);
  const result = r.results?.[type] ?? { data: r.data, next: r.next };
  const list = resourceList(result.data ?? []);
  return {
    [kind]: kind === "shows" ? list.map((x) => toShow(x, !!x.attributes?.inLibrary)) : list.map(toTrack),
    more: !!result.next
  };
}
async function subscribeShow({ id, on }) {
  await session(true);
  numericID(id);
  await rpc("library.set", { kind: "podcasts", id, on });
  pages.clear();
}
async function likeEpisode({ id, on }) {
  await session(true);
  numericID(id);
  await rpc("library.set", { kind: "podcast-episodes", id, on });
}
async function home() {
  staleAt = void 0;
  regionFiltered = false;
  const s = await session(false, true);
  if (!s.authorized)
    return {
      title: host.t("Apple Podcasts"),
      description: host.t(
        "Sign in to load your library, saved episodes and listening progress."
      ),
      sections: []
    };
  const [tracks, following] = await Promise.all([
    continuing(),
    library("podcasts")
  ]);
  const followed = following.map((item) => toShow(item, true));
  return {
    title: host.t("Apple Podcasts"),
    description: syncDescription() ?? (!host.capabilities?.libraryRefresh ? host.t(
      "This MyGo build cannot automatically refresh your library. Install the updated companion MyGo build."
    ) : !host.capabilities?.playbackProgress ? host.t(
      "This MyGo build cannot report playback progress. Use the companion MyGo build for resume and progress sync."
    ) : void 0),
    sections: [
      {
        title: host.t("Continue Listening"),
        tracks: tracks.map(toTrack),
        morePage: "continue",
        empty: host.t("Nothing to continue")
      },
      {
        title: host.t("Following"),
        shows: followed,
        empty: host.t("No followed shows")
      }
    ]
  };
}
async function browse({ page }) {
  if (page === "continue")
    return {
      title: host.t("Continue Listening"),
      sections: [{ tracks: (await continuing()).map(toTrack) }]
    };
  if (page === "latest") {
    await session(true);
    const r = await get(
      `/v1/me/library/latest-episodes?${epQuery}&limit=60`,
      true
    );
    return {
      title: host.t("Latest Episodes"),
      sections: [{ tracks: resourceList(r.data).map(toTrack) }]
    };
  }
  if (page === "account") return home();
  if (page === "sync") {
    const s = await session(true);
    const sync = await rpc("sync.status");
    const state = nativeProgress?.context === s.id ? { trackId: nativeProgress.id } : void 0;
    const links = state?.trackId ? [
      {
        title: host.t("Mark as Played"),
        page: `completed:${state.trackId}:1`,
        icon: "check"
      },
      {
        title: host.t("Mark as Unplayed"),
        page: `completed:${state.trackId}:0`,
        icon: "repeat"
      }
    ] : [];
    if (state?.trackId)
      links.unshift({
        title: host.t("Episode Listening Status"),
        page: `episode:${state.trackId}`,
        icon: "podcast"
      });
    const pending2 = sync.pending.flatMap((op) => {
      const action = op.kind === "position" ? "Sync Playback Position" : op.kind === "completed" ? op.on ? "Mark as Played" : "Mark as Unplayed" : op.kind === "podcasts" ? op.on ? "Follow" : "Unfollow" : op.on ? "Save Episode" : "Unsave Episode";
      return [
        {
          title: `${host.t("Retry")}: ${host.t(action)} \xB7 ${op.item}`,
          subtitle: op.created,
          page: `retry:${op.id}`
        },
        {
          title: `${host.t("Discard Pending Change")} \xB7 ${op.item}`,
          page: `discard:${op.id}`
        }
      ];
    });
    return {
      title: host.t("Playback and Sync"),
      description: s.offline ? syncDescription() : host.t("Changes are confirmed against your Apple Podcasts account."),
      sections: [
        {
          title: host.t("Unconfirmed Changes"),
          links: pending2,
          empty: host.t("All changes are confirmed")
        },
        {
          title: host.t("Current Episode"),
          links,
          empty: host.t("Play an episode to manage its completion state")
        }
      ]
    };
  }
  const episodePage = /^episode:(\d+)$/.exec(page);
  if (episodePage) {
    const item = await episode(episodePage[1]), track = toTrack(item);
    const cloud = item.relationships?.["playback-position"]?.data?.[0]?.attributes;
    const position = Number(cloud?.positionInMilliseconds) || 0;
    const status = host.t(
      cloud?.completed ? "Played" : position > 0 ? "In Progress" : "Unplayed"
    );
    return {
      title: track.name,
      description: `${host.t("Listening Status")}: ${status}
${host.t("Cloud Position")}: ${Math.floor(position / 1e3)} ${host.t("seconds")}

${track.description}`,
      sections: [
        { tracks: [track] },
        {
          links: [
            { title: host.t("Mark as Played"), page: `completed:${item.id}:1` },
            {
              title: host.t("Mark as Unplayed"),
              page: `completed:${item.id}:0`
            }
          ]
        }
      ]
    };
  }
  const pendingAction = /^(retry|discard):([a-f0-9]{24})$/.exec(page);
  if (pendingAction) {
    await session(true);
    await rpc(`sync.${pendingAction[1]}`, { operation: pendingAction[2] });
    return browse({ page: "sync" });
  }
  const complete = /^completed:(\d+):([01])$/.exec(page);
  if (complete) {
    await session(true);
    await rpc("completion.set", { id: complete[1], on: complete[2] === "1" });
    return {
      title: host.t("Playback and Sync"),
      description: host.t("Synced with Apple Podcasts"),
      sections: []
    };
  }
  throw new PluginError("unsupported", host.t("This page is not available"));
}
async function stream({ id }) {
  numericID(id);
  const generation = streamEpoch, key = `${generation}:${id}`;
  if (pendingStreams.has(key)) return pendingStreams.get(key);
  const pending2 = rpc("media.stream", {
    id
  }).then((result) => {
    if (generation !== streamEpoch)
      throw new PluginError(
        "conflict",
        host.t(
          "The Apple Podcasts account changed. Refresh before continuing."
        )
      );
    return result;
  });
  pendingStreams.set(key, pending2);
  try {
    return await pending2;
  } finally {
    if (pendingStreams.get(key) === pending2) pendingStreams.delete(key);
  }
}
async function playbackStart({ id }) {
  numericID(id);
  return rpc("progress.start", { id });
}
async function playbackProgress(progress) {
  if (!progress.context) return;
  nativeProgress = progress;
  await rpc("progress.record", progress);
  if (progress.reason === "quit") await rpc("progress.flush");
}
export {
  account,
  browse,
  home,
  isLiked,
  likeEpisode,
  liked,
  playbackProgress,
  playbackStart,
  recent,
  search,
  setSetting,
  show,
  shows,
  signIn,
  signOut,
  stream,
  subscribeShow
};
