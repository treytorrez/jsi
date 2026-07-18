// KV session store (M1.2, SP/1). Two keys per session — `s:{TOKEN}` holds
// {"offer":{…}}, `a:{TOKEN}` holds {"answer":{…}} — so a fast offer→answer
// exchange never hits KV's 1 write/s/key limit. Every entry carries
// expirationTtl: TTL is the only forget mechanism (D5); there are no deletes.

export interface Description {
  type: "offer" | "answer";
  sdp: string;
}

const offerKey = (token: string): string => `s:${token}`;
const answerKey = (token: string): string => `a:${token}`;

export function putOffer(kv: KVNamespace, token: string, offer: Description, ttl: number): Promise<void> {
  return kv.put(offerKey(token), JSON.stringify({ offer }), { expirationTtl: ttl });
}

export function putAnswer(kv: KVNamespace, token: string, answer: Description, ttl: number): Promise<void> {
  return kv.put(answerKey(token), JSON.stringify({ answer }), { expirationTtl: ttl });
}

export function getOffer(kv: KVNamespace, token: string): Promise<Description | null> {
  return read(kv, offerKey(token), "offer");
}

export function getAnswer(kv: KVNamespace, token: string): Promise<Description | null> {
  return read(kv, answerKey(token), "answer");
}

export async function hasSession(kv: KVNamespace, token: string): Promise<boolean> {
  return (await kv.get(offerKey(token))) !== null;
}

async function read(kv: KVNamespace, key: string, field: "offer" | "answer"): Promise<Description | null> {
  const raw = await kv.get(key);
  if (raw === null) return null;
  try {
    return (JSON.parse(raw) as Record<string, Description>)[field] ?? null;
  } catch {
    return null; // corrupt entry — treat as absent
  }
}
