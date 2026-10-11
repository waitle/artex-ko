// OAuth credentials and the upstream URL are owned exclusively by the server.
export function pollCodexDevice(options: {
  deadline: number;
  interval: number;
  poll: () => Promise<{ connected: boolean; pending: boolean }>;
  onState: (state: "connected" | "expired" | "cancelled") => void;
  onError: (error: Error) => void;
  now?: () => number;
  schedule?: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>;
  unschedule?: (timer: ReturnType<typeof setTimeout>) => void;
}) {
  const now = options.now ?? Date.now;
  const schedule = options.schedule ?? setTimeout;
  const unschedule = options.unschedule ?? clearTimeout;
  let stopped = false;
  let timer: ReturnType<typeof setTimeout>;
  let expiryTimer: ReturnType<typeof setTimeout>;
  const stop = () => {
    stopped = true;
    unschedule(timer);
    unschedule(expiryTimer);
  };
  const finish = (state: "connected" | "expired" | "cancelled") => {
    stop();
    options.onState(state);
  };
  const queue = () => {
    timer = schedule(tick, Math.max(0, Math.min(options.interval, options.deadline - now())));
  };
  const tick = async () => {
    if (stopped) return;
    if (now() >= options.deadline) {
      finish("expired");
      return;
    }
    try {
      const result = await options.poll();
      if (stopped) return;
      if (now() >= options.deadline) {
        finish("expired");
        return;
      }
      if (result.connected) {
        finish("connected");
        return;
      }
      if (!result.pending) {
        finish("cancelled");
        return;
      }
      queue();
    } catch (error) {
      if (stopped) return;
      stop();
      options.onError(error instanceof Error ? error : new Error(String(error)));
    }
  };
  expiryTimer = schedule(
    () => {
      if (!stopped) finish("expired");
    },
    Math.max(0, options.deadline - now()),
  );
  queue();
  return stop;
}

export function codexProxy(format: string, proxy: string) {
  return format === "openai-codex" ? "" : proxy;
}

export function codexTransport(format: string, base_url: string, api_key: string, streaming: boolean) {
  return format === "openai-codex" ? { base_url: "", api_key: "", streaming: true } : { base_url, api_key, streaming };
}
