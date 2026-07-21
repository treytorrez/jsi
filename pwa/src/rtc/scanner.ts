// Camera QR scanner — uses native BarcodeDetector where available (Chrome/
// Edge/Android), falls back to jsQR (Safari/Firefox). Returns the decoded
// text string from the first QR found in the camera feed.

import jsQR from "jsqr";

// scanQR opens the camera and scans until a QR is found, the signal aborts,
// or an error occurs. Resolves with the decoded text.
export async function scanQR(
  video: HTMLVideoElement,
  signal?: AbortSignal,
): Promise<string> {
  const stream = await navigator.mediaDevices.getUserMedia({
    video: { facingMode: "environment" },
    audio: false,
  });
  video.srcObject = stream;
  // iOS requires these attributes set before play().
  video.setAttribute("playsinline", "true");
  video.muted = true;

  // Wait for the video to actually have frames.
  await new Promise<void>((resolve, reject) => {
    if (video.readyState >= 2) {
      resolve();
      return;
    }
    video.onloadeddata = () => resolve();
    setTimeout(() => reject(new Error("camera timed out starting")), 5000);
  });

  await video.play().catch(() => {});

  // Try BarcodeDetector first (Chrome/Edge/Android). If it detects nothing
  // after 2 seconds, fall back to jsQR (some BarcodeDetector impls are buggy).
  const detector = await getBarcodeDetector();
  if (detector) {
    try {
      return await Promise.race([
        scanWithDetector(detector, video, signal),
        new Promise<string>((_, reject) =>
          setTimeout(() => reject(new Error("detector-timeout")), 3000),
        ),
      ]);
    } catch {
      // Fall through to jsQR.
    }
  }

  return scanWithJsQR(video, signal);
}

async function getBarcodeDetector(): Promise<
  { detect: (source: CanvasImageSource) => Promise<Array<{ rawValue: string }>> } | null
> {
  try {
    const Ctor = (window as unknown as { BarcodeDetector?: new (opts: unknown) => unknown }).BarcodeDetector;
    if (!Ctor) return null;
    const det = new Ctor({ formats: ["qr_code"] }) as {
      detect: (source: CanvasImageSource) => Promise<Array<{ rawValue: string }>>;
    };
    // Verify it actually works.
    const c = document.createElement("canvas");
    c.width = 1;
    c.height = 1;
    await det.detect(c);
    return det;
  } catch {
    return null;
  }
}

async function scanWithDetector(
  detector: { detect: (s: CanvasImageSource) => Promise<Array<{ rawValue: string }>> },
  video: HTMLVideoElement,
  signal?: AbortSignal,
): Promise<string> {
  return new Promise((resolve, reject) => {
    let raf = 0;
    const tick = async () => {
      if (signal?.aborted) {
        reject(new DOMException("Aborted", "AbortError"));
        return;
      }
      try {
        if (video.videoWidth > 0) {
          const results = await detector.detect(video);
          if (results && results.length > 0 && results[0].rawValue) {
            resolve(results[0].rawValue);
            return;
          }
        }
      } catch {
        // not ready — keep trying
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    signal?.addEventListener("abort", () => cancelAnimationFrame(raf));
  });
}

async function scanWithJsQR(
  video: HTMLVideoElement,
  signal?: AbortSignal,
): Promise<string> {
  const canvas = document.createElement("canvas");
  const ctx = canvas.getContext("2d", { willReadFrequently: true })!;

  return new Promise((resolve, reject) => {
    let raf = 0;
    const tick = () => {
      if (signal?.aborted) {
        reject(new DOMException("Aborted", "AbortError"));
        return;
      }
      // Need actual video dimensions (not 0).
      if (video.videoWidth > 0 && video.videoHeight > 0 && video.readyState >= 2) {
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        try {
          const imageData = ctx.getImageData(0, 0, canvas.width, canvas.height);
          // attemptBoth — some screens render QR with inverted contrast.
          const code = jsQR(imageData.data, imageData.width, imageData.height, {
            inversionAttempts: "attemptBoth",
          });
          if (code && code.data) {
            resolve(code.data);
            return;
          }
        } catch {
          // canvas not ready — keep trying
        }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    signal?.addEventListener("abort", () => cancelAnimationFrame(raf));
  });
}
