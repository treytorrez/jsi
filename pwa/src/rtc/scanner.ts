// Camera QR scanner — uses native BarcodeDetector where available (Chrome/
// Edge/Android), falls back to jsQR (Safari/Firefox). Returns the decoded
// text string from the first QR found in the camera feed.

import jsQR from "jsqr";

export interface ScannerHandle {
  stop: () => void;
}

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
  video.setAttribute("playsinline", "true");
  await video.play();

  // Try BarcodeDetector first (Chrome/Edge/Android).
  const detector = await getBarcodeDetector();
  if (detector) {
    return scanWithDetector(detector, video, signal);
  }

  // Fallback: jsQR on canvas frames (Safari/Firefox).
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
    // Verify it actually works (some browsers have the constructor but not the impl).
    await det.detect(createBlankCanvas());
    return det;
  } catch {
    return null;
  }
}

function createBlankCanvas(): CanvasImageSource {
  const c = document.createElement("canvas");
  c.width = 1;
  c.height = 1;
  return c;
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
        const results = await detector.detect(video);
        if (results && results.length > 0 && results[0].rawValue) {
          resolve(results[0].rawValue);
          return;
        }
      } catch {
        // detector not ready yet — keep trying
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
  const ctx = canvas.getContext("2d")!;

  return new Promise((resolve, reject) => {
    let raf = 0;
    const tick = () => {
      if (signal?.aborted) {
        reject(new DOMException("Aborted", "AbortError"));
        return;
      }
      if (video.readyState === video.HAVE_CURRENT_DATA) {
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        const imageData = ctx.getImageData(0, 0, canvas.width, canvas.height);
        const code = jsQR(imageData.data, imageData.width, imageData.height, {
          inversionAttempts: "dontInvert",
        });
        if (code && code.data) {
          resolve(code.data);
          return;
        }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    signal?.addEventListener("abort", () => cancelAnimationFrame(raf));
  });
}
