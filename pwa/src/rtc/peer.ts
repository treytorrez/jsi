// RTCPeerConnection wrapper — non-trickle ICE (D2). Browser-native.
// Waits for icegatheringstate === "complete" before returning the local
// description (all candidates embedded). Same pattern as internal/peer.

export interface PeerConfig {
  iceServers: RTCIceServer[];
  enableMDNS?: boolean;
}

export class Peer {
  pc: RTCPeerConnection;
  channel: RTCDataChannel | null = null;
  private gatherComplete: Promise<void>;

  constructor(cfg: PeerConfig) {
    // mDNS is browser-controlled: the browser decides based on its settings.
    // We pass the iceServers; mDNS host candidates are always gathered by the
    // browser. The enableMDNS flag is informational — the browser handles it.
    this.pc = new RTCPeerConnection({ iceServers: cfg.iceServers });
    this.gatherComplete = new Promise((resolve) => {
      const check = () => {
        if (this.pc.iceGatheringState === "complete") {
          this.pc.removeEventListener("icegatheringstatechange", check);
          resolve();
        }
      };
      this.pc.addEventListener("icegatheringstatechange", check);
    });
  }

  // Offerer: create the data channel first (so the offer has m=application),
  // then create the offer and gather.
  async createOffer(signal?: AbortSignal): Promise<RTCSessionDescriptionInit> {
    this.channel = this.pc.createDataChannel("jsi", { ordered: true });
    this.channel.binaryType = "arraybuffer";
    // Install a buffering handler immediately so messages arriving before
    // the transfer engine sets up its inbox aren't lost.
    this.installMessageBuffer();
    const offer = await this.pc.createOffer();
    await this.pc.setLocalDescription(offer);
    await this.waitForGather(signal);
    return this.pc.localDescription!.toJSON();
  }

  // Answerer: set the remote offer, create the answer, gather.
  async createAnswer(
    offer: RTCSessionDescriptionInit,
    signal?: AbortSignal,
  ): Promise<RTCSessionDescriptionInit> {
    this.pc.ondatachannel = (e) => {
      this.channel = e.channel;
      this.channel.binaryType = "arraybuffer";
      // Install a buffering handler immediately so messages arriving before
      // the transfer engine sets up its inbox aren't lost.
      this.installMessageBuffer();
    };
    await this.pc.setRemoteDescription(offer);
    const answer = await this.pc.createAnswer();
    await this.pc.setLocalDescription(answer);
    await this.waitForGather(signal);
    return this.pc.localDescription!.toJSON();
  }

  async setRemote(desc: RTCSessionDescriptionInit): Promise<void> {
    await this.pc.setRemoteDescription(desc);
  }

  // waitOpen resolves when the data channel opens. Handles the race where
  // ondatachannel hasn't fired yet (channel is null) by also listening for
  // the "datachannel" event on the PeerConnection. Also rejects on channel
  // error/close or connection failure so the caller doesn't hang forever.
  waitOpen(signal?: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        this.channel?.removeEventListener("open", onChannelOpen);
        this.channel?.removeEventListener("error", onChannelError);
        this.channel?.removeEventListener("close", onChannelClose);
        this.pc.removeEventListener("datachannel", onDataChannel);
        this.pc.removeEventListener("connectionstatechange", onConnStateChange);
        signal?.removeEventListener("abort", onAbort);
      };

      const onChannelOpen = () => { cleanup(); resolve(); };
      const onChannelError = () => { cleanup(); reject(new Error("data channel error")); };
      const onChannelClose = () => { cleanup(); reject(new Error("data channel closed")); };
      const onAbort = () => { cleanup(); reject(new DOMException("Aborted", "AbortError")); };

      const onDataChannel = (e: RTCDataChannelEvent) => {
        this.channel = e.channel;
        attachChannelListeners();
      };

      const onConnStateChange = () => {
        if (this.pc.connectionState === "failed") {
          cleanup();
          reject(new Error("ICE connection failed"));
        }
      };

      const attachChannelListeners = () => {
        if (!this.channel) return;
        if (this.channel.readyState === "open") {
          cleanup();
          resolve();
          return;
        }
        this.channel.addEventListener("open", onChannelOpen);
        this.channel.addEventListener("error", onChannelError);
        this.channel.addEventListener("close", onChannelClose);
      };

      // Listen for datachannel in case it hasn't arrived yet (race fix).
      this.pc.addEventListener("datachannel", onDataChannel);
      this.pc.addEventListener("connectionstatechange", onConnStateChange);
      // If channel already exists, attach listeners now.
      attachChannelListeners();
      signal?.addEventListener("abort", onAbort);
    });
  }

  // connectionPath reads the selected candidate pair for D15 transparency.
  async connectionPath(): Promise<string> {
    const stats = await this.pc.getStats();
    let path = "unknown";
    stats.forEach((report) => {
      if (report.type === "candidate-pair" && (report as RTCIceCandidatePairStats).nominated) {
        const local = stats.get((report as RTCIceCandidatePairStats).localCandidateId);
        if (local && (local as Record<string, unknown>).candidateType) {
          const typ = (local as Record<string, string>).candidateType;
          switch (typ) {
            case "host":
              path = "direct (host)";
              break;
            case "srflx":
              path = "via STUN (srflx)";
              break;
            case "relay":
              path = "TURN RELAY (Cloudflare carries encrypted bytes)";
              break;
            default:
              path = typ;
          }
        }
      }
    });
    return path;
  }

  close() {
    this.channel?.close();
    this.pc.close();
  }

  // --- Early message buffering ---
  // Messages that arrive before the transfer engine sets up its inbox are
  // buffered here. The transfer engine calls drainBufferedMessages() to
  // claim them when it sets up its own onmessage handler.
  private bufferedMessages: Array<{ data: ArrayBuffer | string }> = [];
  private buffering = false;

  private installMessageBuffer(): void {
    if (!this.channel || this.buffering) return;
    this.buffering = true;
    this.channel.onmessage = (e: MessageEvent) => {
      this.bufferedMessages.push({ data: e.data });
    };
  }

  drainBufferedMessages(): Array<{ data: ArrayBuffer | string }> {
    const msgs = this.bufferedMessages;
    this.bufferedMessages = [];
    this.buffering = false;
    return msgs;
  }

  // Returns the channel after ensuring it's ready. The transfer engine
  // should call drainBufferedMessages() AFTER setting up its own handler
  // to process any messages that arrived during connection setup.
  getChannel(): RTCDataChannel | null {
    return this.channel;
  }

  private async waitForGather(signal?: AbortSignal): Promise<void> {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
    // Race gathering against a 10s timeout. TURN allocation can take 2-5s
    // on top of STUN; 3s (the CLI default) was too short for the browser
    // when TURN is in the ICE server list. 10s is generous but bounded.
    const timeout = new Promise<void>((resolve) => setTimeout(resolve, 10000));
    await Promise.race([this.gatherComplete, timeout]);
  }
}
