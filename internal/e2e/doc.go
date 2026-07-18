// Package e2e holds end-to-end integration tests that exercise the full
// client stack (signal + peer + transfer) against external infrastructure:
// M2.8 runs the SP/1 handshake and a TP/1 transfer through a real signaling
// worker (see proto/SIGNALING.md, PLAN.md §8–9). The tests skip unless the
// JSI_E2E_WORKER base-URL env var is set; scripts/e2e-local.sh boots a
// local worker and sets it.
package e2e
