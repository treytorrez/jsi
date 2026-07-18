package cli

import (
	"io"
	"strings"

	"github.com/mdp/qrterminal/v3"
)

// QRPayload chooses what the send-side terminal QR encodes (M3.1, PLAN.md
// §7.5): with --pwa-url, "<base>#t=<token>" for scan-to-receive in the PWA;
// otherwise the bare token (the default until S4 lands).
func QRPayload(pwaURL, tok string) string {
	if pwaURL == "" {
		return tok
	}
	return strings.TrimRight(pwaURL, "/") + "#t=" + tok
}

// printQR renders payload as a half-block terminal QR (mdp/qrterminal/v3,
// EC level M per PLAN.md §2) on w.
func printQR(w io.Writer, payload string) {
	qrterminal.GenerateHalfBlock(payload, qrterminal.M, w)
}
