export class PluginError extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message);
  }
}
type Connection = {
  url: string;
  token: string;
  protocol: number;
  process: HostProcess;
};
let pending: Promise<Connection> | undefined;
const timeout = <T>(promise: Promise<T>, ms: number): Promise<T> =>
  new Promise((resolve, reject) => {
    const id = setTimeout(
      () =>
        reject(
          new PluginError(
            'setup',
            host.t('The Apple Podcasts helper did not respond'),
          ),
        ),
      ms,
    );
    promise.then(
      (value) => {
        clearTimeout(id);
        resolve(value);
      },
      (error) => {
        clearTimeout(id);
        reject(error);
      },
    );
  });
async function connect(): Promise<Connection> {
  if (pending) return pending;
  pending = (async () => {
    const executable =
      host.platform === 'windows'
        ? 'apple-podcasts-helper.exe'
        : 'apple-podcasts-helper';
    const args = ['--data-dir', host.dataDir];
    const chrome = host.setting('chromePath')?.trim();
    if (chrome) args.push('--chrome', chrome);
    let process: HostProcess;
    try {
      process = host.spawn(`${host.dir}/bin/${executable}`, args);
    } catch {
      throw new PluginError(
        'setup',
        host.t('Allow Apple Podcasts to start its helper in plugin settings'),
      );
    }
    try {
      const line = await timeout(process.readLine(), 15000);
      if (!line) throw new Error('No handshake');
      const data = JSON.parse(line) as {
        url: string;
        token: string;
        protocol: number;
      };
      if (
        data.protocol !== 1 ||
        !/^http:\/\/127\.0\.0\.1:\d{1,5}$/.test(data.url) ||
        !/^[0-9a-f]{64}$/.test(data.token)
      )
        throw new Error('Invalid handshake');
      const port = Number(data.url.split(':').at(-1));
      if (port < 1 || port > 65535) throw new Error('Invalid port');
      const connection = { ...data, process };
      const active = pending;
      void process.wait().then(
        () => {
          if (pending === active) pending = undefined;
        },
        () => {
          if (pending === active) pending = undefined;
        },
      );
      return connection;
    } catch {
      process.kill();
      throw new PluginError(
        'setup',
        host.t(
          'The Apple Podcasts helper could not start. Reinstall the plugin and check Chrome.',
        ),
      );
    }
  })();
  try {
    return await pending;
  } catch (error) {
    pending = undefined;
    throw error;
  }
}
export async function disconnect(): Promise<void> {
  const existing = pending;
  if (!existing) return;
  try {
    const c = await existing;
    c.process.closeStdin();
    try {
      await timeout(c.process.wait(), 20000);
    } catch {
      c.process.kill();
    }
  } finally {
    if (pending === existing) pending = undefined;
  }
}
export async function rpc<T = any>(
  method: string,
  params: Record<string, unknown> = {},
): Promise<T> {
  const connection = await connect();
  let response: Response;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 175000);
  try {
    response = await fetch(`${connection.url}/rpc`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${connection.token}`,
        'content-type': 'application/json',
      },
      body: JSON.stringify({ method, params }),
      signal: controller.signal,
    });
  } catch {
    throw new PluginError(
      'network',
      host.t(
        'Apple Podcasts connection failed. Check the official window and retry.',
      ),
    );
  } finally {
    clearTimeout(timer);
  }
  if (!response.ok)
    throw new PluginError(
      'setup',
      host.t('The Apple Podcasts helper rejected the connection'),
    );
  const result = (await response.json()) as {
    ok: boolean;
    data: T;
    error?: { code: string; message: string; pending?: boolean };
  };
  if (!result.ok)
    throw new PluginError(
      result.error?.code ?? 'unavailable',
      host.t(
        result.error?.message ??
          'Apple Podcasts could not complete the operation',
      ) +
        (result.error?.pending
          ? ' ' +
            host.t(
              'The change is unconfirmed. Open Playback and Sync to retry or discard it.',
            )
          : ''),
    );
  return result.data;
}
