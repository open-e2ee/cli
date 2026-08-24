#!/usr/bin/env node

import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import { createSignalProtocolClient } from "@open-e2ee/signal-protocol-sdk";
import { inMemoryStore } from "@open-e2ee/signal-protocol-sdk/local/store/memory";
import { inMemoryRelay } from "@open-e2ee/signal-protocol-sdk/remote/relay/memory";

const args = process.argv.slice(2);
const skipIndex = args.indexOf("--skip-roundtrip");
const skipRoundtrip = skipIndex !== -1;
if (skipRoundtrip) args.splice(skipIndex, 1);

const require = createRequire(import.meta.url);
const oe = require.resolve("@open-e2ee/cli/bin/oe.js");
const initialized = spawnSync(process.execPath, [oe, "init", ...args], {
  stdio: "inherit",
});
if (initialized.error || initialized.status !== 0) {
  process.exit(initialized.status ?? 1);
}

if (!skipRoundtrip) {
  const relay = inMemoryRelay();
  await relay.registerDevice("alice", {
    encryptedDeviceName: new ArrayBuffer(0),
  });
  await relay.registerDevice("bob", {
    encryptedDeviceName: new ArrayBuffer(0),
  });
  const alice = await createSignalProtocolClient({
    identity: { userId: "alice" },
    adapters: { storage: inMemoryStore(), relay },
  });
  const bob = await createSignalProtocolClient({
    identity: { userId: "bob" },
    adapters: { storage: inMemoryStore(), relay },
  });
  const delivered = new Promise((resolve) => {
    bob.registerHook("onMessageDecrypted", async (message) => {
      process.stdout.write(`${message.senderId}: ${message.content}\n`);
      bob.stopRelaySubscription();
      resolve();
    });
  });
  await alice.send("bob", "hello");
  bob.startRelaySubscription();
  await delivered;
}
