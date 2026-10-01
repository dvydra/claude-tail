import { afterAll, expect, mock, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import * as os from "node:os";
import { join } from "node:path";

const home = mkdtempSync(join(os.tmpdir(), "entire-tail-plugin-"));
mock.module("node:os", () => ({ ...os, homedir: () => home }));
const { default: entireTail } = await import("./entire-tail.ts");

afterAll(() => {
  rmSync(home, { recursive: true, force: true });
});

const message = (id: number) => ({
  id: `M-${id}`,
  role: id % 2 === 0 ? "user" : "assistant",
  content: [{ type: "text", text: `message ${id}` }],
});

const waitFor = async (condition: () => boolean): Promise<void> => {
  for (let i = 0; i < 100; i++) {
    if (condition()) return;
    await Bun.sleep(10);
  }
  throw new Error("timed out waiting for plugin work");
};

test("idle backfills paginated messages when agent.end is absent", async () => {
  const handlers = new Map<string, (...args: any[]) => unknown>();
  const disposers: Array<() => void> = [];
  const calls: Array<Record<string, unknown>> = [];
  let stateSubscriber: ((state: string) => void) | undefined;
  let unsubscribed = false;
  let messages = [message(1), message(2), message(3)];
  const thread = {
    id: "T-idle-backfill",
    messages: async (options: { offset?: number; limit: number }) => {
      calls.push(options);
      const end = Math.max(0, messages.length - (options.offset ?? 0));
      return messages.slice(Math.max(0, end - options.limit), end);
    },
    state: {
      subscribe(callback: (state: string) => void) {
        stateSubscriber = callback;
        return { unsubscribe: () => (unsubscribed = true) };
      },
    },
  };
  const amp = {
    on: (name: string, handler: (...args: any[]) => unknown) => handlers.set(name, handler),
    onDispose: (dispose: () => void) => disposers.push(dispose),
  };
  const ctx = { thread, system: { executor: { kind: "local" } } };

  entireTail(amp as any);
  handlers.get("session.start")?.({}, ctx);
  await waitFor(() => calls.length === 1);

  messages = Array.from({ length: 25 }, (_, i) => message(i + 1));
  expect(stateSubscriber).toBeDefined();
  stateSubscriber?.("idle");

  const feed = join(home, ".cache", "entire-tail", "amp", "live", `${thread.id}.jsonl`);
  await waitFor(() => existsSync(feed) && readFileSync(feed, "utf8").trim().split("\n").length === 22);
  const lines = readFileSync(feed, "utf8").trim().split("\n").map((line) => JSON.parse(line));

  expect(lines.map((line) => line.message.protocolMessageID)).toEqual(
    Array.from({ length: 22 }, (_, i) => `M-${i + 4}`),
  );
  expect(lines.at(-1).idleFinal).toBe(true);
  expect(calls.slice(1)).toEqual([
    { full: true, from: "end", offset: 0, limit: 20 },
    { full: true, from: "end", offset: 20, limit: 20 },
  ]);

  disposers.forEach((dispose) => dispose());
  expect(unsubscribed).toBe(true);
});
