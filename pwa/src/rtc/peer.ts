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

  waitOpen(signal?: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      if (this.channel && this.channel.readyState === "open") {
        resolve();
        return;
      }
      const onOpen = () => {
        cleanup();
        resolve();
      };
      const onAbort = () => {
        cleanup();
        reject(new DOMException("Aborted", "AbortError"));
      };
      const cleanup = () => {
        this.channel?.removeEventListener("open", onOpen);
        signal?.removeEventListener("abort", onAbort);
      };
      this.channel?.addEventListener("open", onOpen);
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

  private async waitForGather(signal?: AbortSignal): Promise<void> {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
    // Race gathering against a 3s timeout (D9: ship partial SDP on unreachable STUN).
    const timeout = new Promise<void>((resolve) => setTimeout(resolve, 3000));
    await Promise.race([this.gatherComplete, timeout]);
  }
}
