import { describe, it, expect } from "vitest";
import {
  marshal,
  unmarshal,
  sanitizeName,
  ChunkSize,
  Version,
  type Message,
} from "./protocol";

describe("protocol", () => {
  it("marshals hello", () => {
    const msg: Message = { type: "hello", v: Version, app: "jsi-pwa/0.1.0" };
    const json = marshal(msg);
    expect(json).toContain('"type":"hello"');
    expect(json).toContain('"v":1');
    expect(unmarshal(json)).toEqual(msg);
  });

  it("marshals manifest with files", () => {
    const msg: Message = {
      type: "manifest",
      files: [{ id: 0, name: "test.bin", size: 1024 }],
    };
    const round = unmarshal(marshal(msg));
    expect(round).toEqual(msg);
  });

  it("accept with no files means accept-all", () => {
    const msg: Message = { type: "accept" };
    expect(marshal(msg)).toBe('{"type":"accept"}');
  });

  it("file-end carries sha256", () => {
    const msg: Message = {
      type: "file-end",
      id: 0,
      sha256: "abc123",
    };
    expect(unmarshal(marshal(msg))).toEqual(msg);
  });

  it("chunk size is 16 KiB", () => {
    expect(ChunkSize).toBe(16384);
  });

  it("sanitizeName strips paths", () => {
    expect(sanitizeName("/etc/passwd")).toBe("passwd");
    expect(sanitizeName("C:\\Users\\test\\file.txt")).toBe("file.txt");
    expect(sanitizeName("clean.txt")).toBe("clean.txt");
  });

  it("sanitizeName rejects unsafe names", () => {
    expect(() => sanitizeName("..")).toThrow();
    expect(() => sanitizeName(".")).toThrow();
    expect(() => sanitizeName("")).toThrow();
    expect(() => sanitizeName("a\0b")).toThrow();
  });
});
