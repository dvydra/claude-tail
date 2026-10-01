// entire-tail feed for Amp: appends each settled thread message to
// ~/.cache/entire-tail/amp/live/<thread>.jsonl so `entire-tail --agent amp`
// follows a local file instead of polling `amp threads export`. Lines use the
// ampEnvelope shape that adapter_amp.go's normalizeAmp already renders.
import type { PluginAPI, PluginThread, Subscription, ThreadMessage } from "@ampcode/plugin";
import { appendFileSync, mkdirSync, readFileSync, watch, type FSWatcher } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const liveDir = join(homedir(), ".cache", "entire-tail", "amp", "live");
const pageSize = 20;

export default function (amp: PluginAPI) {
  const emitted = new Map<string, Set<string>>();
  const queues = new Map<string, Promise<void>>();
  const stateSubscriptions = new Map<string, Subscription>();

  // Newest-first pages until one reaches a message already emitted, so a burst
  // of messages between two events is never skipped.
  const unseen = async (thread: PluginThread, seen: Set<string>): Promise<ThreadMessage[]> => {
    const fresh: ThreadMessage[] = [];
    for (let offset = 0; ; offset += pageSize) {
      const page = await thread.messages({ full: true, from: "end", offset, limit: pageSize });
      for (let i = page.length - 1; i >= 0; i--) {
        if (seen.has(String(page[i].id))) return fresh.reverse();
        fresh.push(page[i]);
      }
      if (page.length < pageSize) return fresh.reverse();
    }
  };

  const settled = (m: ThreadMessage, last: boolean, final: boolean): boolean => {
    if (m.role === "user") {
      return m.content.every((b) => b.type !== "tool_result" || (b.status !== "running" && b.status !== "pending"));
    }
    if (m.role === "assistant") return final || !last || m.content.some((b) => b.type === "tool_use");
    return true;
  };

  // Exports nest a tool's structured output (e.g. shell's {output, exitCode})
  // directly under run.result; plugins receive that object JSON-encoded.
  // Plain strings and content blocks sit at .output.
  const runResult = (output: unknown): Record<string, unknown> => {
    let value = output;
    if (typeof value === "string" && value.startsWith("{")) {
      try {
        value = JSON.parse(value);
      } catch {
        value = output;
      }
    }
    return value && typeof value === "object" && !Array.isArray(value) ? { ...value } : { output };
  };

  const envelope = (m: ThreadMessage, idleFinal: boolean): string => {
    const content = m.content.map((b) =>
      b.type === "tool_result"
        ? { type: "tool_result", toolUseID: b.toolUseID, run: { status: b.status, result: runResult(b.output) } }
        : b,
    );
    return JSON.stringify({
      agent: "amp",
      message: {
        role: m.role,
        createdAt: new Date().toISOString(),
        protocolMessageID: String(m.id),
        state: { type: "complete" },
        content,
      },
      ...(idleFinal ? { idleFinal: true } : {}),
    });
  };

  // `turn` is agent.end's own message list: thread.messages() still lags the
  // turn's last messages when agent.end fires.
  const sync = async (thread: PluginThread, final: boolean, turn?: ThreadMessage[]): Promise<void> => {
    const seen = emitted.get(thread.id);
    if (!seen) return;
    const fresh = turn ? turn.filter((m) => !seen.has(String(m.id))) : await unseen(thread, seen);
    const lastAssistant = fresh.map((m) => m.role).lastIndexOf("assistant");
    const lines: string[] = [];
    for (let i = 0; i < fresh.length; i++) {
      const m = fresh[i];
      if (!settled(m, i === fresh.length - 1, final)) break;
      seen.add(String(m.id));
      if (m.role !== "info") lines.push(envelope(m, final && i === lastAssistant));
    }
    if (lines.length === 0) return;
    mkdirSync(liveDir, { recursive: true, mode: 0o700 });
    appendFileSync(join(liveDir, `${thread.id}.jsonl`), lines.join("\n") + "\n", { mode: 0o600 });
  };

  // Everything already in the thread when the plugin first sees it belongs to
  // the export backfill; only `keep` (the prompt that started this turn) is new.
  const seed = async (thread: PluginThread, keep?: string): Promise<void> => {
    if (emitted.has(thread.id)) return;
    const seen = new Set<string>();
    emitted.set(thread.id, seen);
    const feed = join(liveDir, `${thread.id}.jsonl`);
    try {
      for (const line of readFileSync(feed, "utf8").split("\n")) {
        if (!line) continue;
        try {
          const id = JSON.parse(line)?.message?.protocolMessageID;
          if (id) seen.add(String(id));
        } catch {}
      }
    } catch {}
    const page = await thread.messages({ full: true, from: "end", limit: pageSize });
    for (const m of page) if (String(m.id) !== keep) seen.add(String(m.id));
    // An empty feed already switches a waiting tail off export polling.
    mkdirSync(liveDir, { recursive: true, mode: 0o700 });
    appendFileSync(feed, "", { mode: 0o600 });
  };

  // Serialized per thread; a failed read never blocks Amp or the next event.
  const enqueue = (thread: PluginThread, work: () => Promise<void>): Promise<void> => {
    const next = (queues.get(thread.id) ?? Promise.resolve()).then(work).catch(() => {});
    queues.set(thread.id, next);
    return next;
  };

  // Amp's diagnostic log gains a message_added line the moment a message lands,
  // which surfaces a tool call as it starts. tool.call would too, but it is a
  // request event whose `allow` might override another plugin's rejection.
  const watchers = new Map<string, FSWatcher>();
  const watchLog = (thread: PluginThread): void => {
    if (watchers.has(thread.id)) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const watcher = watch(join(homedir(), ".cache", "amp", "logs", "threads", `${thread.id}.log`), () => {
        if (timer) return;
        timer = setTimeout(() => {
          timer = undefined;
          enqueue(thread, () => sync(thread, false));
        }, 500);
      });
      watchers.set(thread.id, watcher);
    } catch {
      // No local log (remote executor or Amp moved it): turn events still sync.
    }
  };

  const local = (ctx: { system: { executor: { kind: string } } }) => ctx.system.executor.kind === "local";
  const watchState = (thread: PluginThread): void => {
    if (stateSubscriptions.has(thread.id)) return;
    const subscription = thread.state.subscribe((state) => {
      if (state === "idle") enqueue(thread, () => sync(thread, true));
    });
    stateSubscriptions.set(thread.id, subscription);
  };
  const start = (thread: PluginThread, keep?: string): void => {
    enqueue(thread, async () => {
      await seed(thread, keep);
      watchLog(thread);
      if (keep !== undefined) await sync(thread, false);
    });
    watchState(thread);
  };

  amp.on("session.start", (_event, ctx) => {
    if (local(ctx)) start(ctx.thread);
  });
  amp.on("agent.start", (event, ctx) => {
    if (local(ctx)) start(ctx.thread, String(event.id));
    return {};
  });
  amp.on("tool.result", async (_event, ctx) => {
    if (local(ctx)) await enqueue(ctx.thread, () => sync(ctx.thread, false));
  });
  // Awaited so `amp -x`, which exits right after the turn, still gets the reply.
  amp.on("agent.end", async (event, ctx) => {
    if (local(ctx)) await enqueue(ctx.thread, () => sync(ctx.thread, true, event.messages));
  });
  amp.onDispose(() => {
    for (const watcher of watchers.values()) watcher.close();
    for (const subscription of stateSubscriptions.values()) subscription.unsubscribe();
  });
}
