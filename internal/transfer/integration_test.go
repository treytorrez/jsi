package transfer_test

// M2.7 integration: two peer.Conns over host-candidate loopback (no ICE
// servers, no mDNS — no external traffic), joined by an in-memory fake
// signal.Channel, running real TP/1 transfers end to end.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/protocol"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// (*peer.Conn) is the production Endpoint; assert the seam holds.
var _ transfer.Endpoint = (*peer.Conn)(nil)

// fakeSignal passes SDPs between the two sides through buffered channels —
// the in-process stand-in for the worker/QR/paste signalers.
type fakeSignal struct {
	offer  chan webrtc.SessionDescription
	answer chan webrtc.SessionDescription
}

var _ signal.Channel = (*fakeSignal)(nil)

func newFakeSignal() *fakeSignal {
	return &fakeSignal{
		offer:  make(chan webrtc.SessionDescription, 1),
		answer: make(chan webrtc.SessionDescription, 1),
	}
}

func (f *fakeSignal) Announce(ctx context.Context, offer webrtc.SessionDescription) (string, func(context.Context) (webrtc.SessionDescription, error), error) {
	select {
	case f.offer <- offer:
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	wait := func(ctx context.Context) (webrtc.SessionDescription, error) {
		select {
		case a := <-f.answer:
			return a, nil
		case <-ctx.Done():
			return webrtc.SessionDescription{}, ctx.Err()
		}
	}
	return "fake-token", wait, nil
}

func (f *fakeSignal) Join(ctx context.Context, _ string) (webrtc.SessionDescription, func(context.Context, webrtc.SessionDescription) error, error) {
	var offer webrtc.SessionDescription
	select {
	case offer = <-f.offer:
	case <-ctx.Done():
		return webrtc.SessionDescription{}, nil, ctx.Err()
	}
	respond := func(ctx context.Context, answer webrtc.SessionDescription) error {
		select {
		case f.answer <- answer:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return offer, respond, nil
}

// testConfig uses host candidates only: the two PeerConnections connect
// over loopback with no external traffic (per internal/peer's test).
var testConfig = peer.Config{ICEServers: nil, EnableMDNS: false}

// connect runs the full non-trickle handshake through the fake signaler and
// returns both ends with the "jsi" channel open.
func connect(t *testing.T, ctx context.Context) (offerer, answerer *peer.Conn) {
	t.Helper()
	sig := newFakeSignal()

	oc, offer, err := peer.Offer(ctx, testConfig)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	token, wait, err := sig.Announce(ctx, offer)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	remoteOffer, respond, err := sig.Join(ctx, token)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	ac, answer, err := peer.Answer(ctx, testConfig, remoteOffer)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if err := respond(ctx, answer); err != nil {
		t.Fatalf("respond: %v", err)
	}
	desc, err := wait(ctx)
	if err != nil {
		t.Fatalf("wait for answer: %v", err)
	}
	if err := oc.SetRemote(ctx, desc); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if err := oc.WaitOpen(ctx); err != nil {
		t.Fatalf("offerer WaitOpen: %v", err)
	}
	if err := ac.WaitOpen(ctx); err != nil {
		t.Fatalf("answerer WaitOpen: %v", err)
	}
	t.Cleanup(func() {
		_ = oc.Close()
		_ = ac.Close()
	})
	return oc, ac
}

// eventLog collects events drained from a transfer event channel.
type eventLog struct {
	mu     sync.Mutex
	events []transfer.Event
}

func (l *eventLog) drain(ev <-chan transfer.Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for e := range ev {
		l.mu.Lock()
		l.events = append(l.events, e)
		l.mu.Unlock()
	}
}

func (l *eventLog) snapshot() []transfer.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]transfer.Event(nil), l.events...)
}

func hasKind(events []transfer.Event, kind transfer.EventKind) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// checkSessionEvents verifies the common event contract for a successful
// session: file-start, at least one progress event, file-done, done, and no
// error event.
func checkSessionEvents(t *testing.T, side string, events []transfer.Event, wantBytes int64) {
	t.Helper()
	for _, kind := range []transfer.EventKind{
		transfer.EventFileStart, transfer.EventProgress, transfer.EventFileDone, transfer.EventDone,
	} {
		if !hasKind(events, kind) {
			t.Errorf("%s: missing %s event", side, kind)
		}
	}
	if hasKind(events, transfer.EventError) {
		t.Errorf("%s: unexpected error event", side)
	}
	var lastProgress, fileDone *transfer.Event
	for i := range events {
		switch events[i].Kind {
		case transfer.EventProgress:
			lastProgress = &events[i]
		case transfer.EventFileDone:
			fileDone = &events[i]
		}
	}
	if lastProgress == nil || lastProgress.BytesDone != wantBytes || lastProgress.BytesTotal != wantBytes {
		t.Errorf("%s: last progress = %+v, want %d/%d", side, lastProgress, wantBytes, wantBytes)
	}
	if fileDone == nil || fileDone.BytesDone != wantBytes || fileDone.Err != nil {
		t.Errorf("%s: file-done = %+v, want %d bytes, nil error", side, fileDone, wantBytes)
	}
}

// writeRandom creates a crypto/rand file of size bytes in dir.
func writeRandom(t *testing.T, dir, name string, size int64) (path string, sum [32]byte) {
	t.Helper()
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path, sha256.Sum256(payload)
}

// TestRoundTrip is the M2.7 acceptance core: 10 MiB of crypto/rand through
// Send/Receive over loopback WebRTC, arriving bit-identical.
func TestRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src, wantSum := writeRandom(t, srcDir, "random.bin", 10<<20)

	snd, rcv := connect(t, ctx)
	files, err := transfer.FilesFromPaths([]string{src})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}

	evS := make(chan transfer.Event, 4096)
	evR := make(chan transfer.Event, 4096)
	var wg sync.WaitGroup
	var logS, logR eventLog
	wg.Add(2)
	go logS.drain(evS, &wg)
	go logR.drain(evR, &wg)

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	go func() { sendErr <- transfer.Send(ctx, snd, files, evS) }()
	go func() { recvErr <- transfer.Receive(ctx, rcv, dstDir, evR) }()

	if err := <-sendErr; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-recvErr; err != nil {
		t.Fatalf("Receive: %v", err)
	}
	wg.Wait()

	got, err := os.ReadFile(filepath.Join(dstDir, "random.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if sum := sha256.Sum256(got); sum != wantSum {
		t.Fatal("received file SHA-256 differs from source")
	}

	checkSessionEvents(t, "sender", logS.snapshot(), 10<<20)
	checkSessionEvents(t, "receiver", logR.snapshot(), 10<<20)
}

// TestCancelMidFlight cancels the receiver mid-transfer: both sides must
// return promptly and the partial file must be absent or incomplete.
func TestCancelMidFlight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	const size = 32 << 20
	src, _ := writeRandom(t, srcDir, "big.bin", size)

	snd, rcv := connect(t, ctx)
	files, err := transfer.FilesFromPaths([]string{src})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}

	recvCtx, recvCancel := context.WithCancel(ctx)
	defer recvCancel()

	evS := make(chan transfer.Event, 4096)
	evR := make(chan transfer.Event, 4096)
	// Drain sender events; Send closes evS on return.
	go func() {
		for range evS { //nolint:revive // discard
		}
	}()
	// Cancel the receiver as soon as bytes start flowing; Receive closes
	// evR on return, which ends this goroutine.
	go func() {
		for e := range evR {
			if e.Kind == transfer.EventProgress {
				recvCancel()
			}
		}
	}()

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	go func() { sendErr <- transfer.Send(ctx, snd, files, evS) }()
	go func() { recvErr <- transfer.Receive(recvCtx, rcv, dstDir, evR) }()

	select {
	case err := <-recvErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Receive: got %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Receive did not return promptly after cancel")
	}

	select {
	case err := <-sendErr:
		if !errors.Is(err, transfer.ErrPeerCancel) {
			t.Fatalf("Send: got %v, want ErrPeerCancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send did not return promptly after peer cancel")
	}

	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		if info.Size() >= size {
			t.Fatalf("partial file %s is %d bytes — indistinguishable from complete", e.Name(), info.Size())
		}
		t.Logf("partial file %s: %d of %d bytes (detectably incomplete)", e.Name(), info.Size(), size)
	}
}

// TestReject: the receiver's Decide accepts nothing — the sender must take
// the reject path and no file bytes may flow.
func TestReject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src, _ := writeRandom(t, srcDir, "unwanted.bin", 1<<20)

	snd, rcv := connect(t, ctx)
	files, err := transfer.FilesFromPaths([]string{src})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}

	evS := make(chan transfer.Event, 64)
	evR := make(chan transfer.Event, 64)
	var wg sync.WaitGroup
	var logS, logR eventLog
	wg.Add(2)
	go logS.drain(evS, &wg)
	go logR.drain(evR, &wg)

	rejectAll := func([]protocol.FileMeta) ([]int, error) { return nil, nil }

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	go func() { sendErr <- transfer.Send(ctx, snd, files, evS) }()
	go func() { recvErr <- transfer.Receive(ctx, rcv, dstDir, evR, transfer.WithDecide(rejectAll)) }()

	if err := <-sendErr; !errors.Is(err, transfer.ErrRejected) {
		t.Fatalf("Send: got %v, want ErrRejected", err)
	}
	if err := <-recvErr; !errors.Is(err, transfer.ErrDeclined) {
		t.Fatalf("Receive: got %v, want ErrDeclined", err)
	}
	wg.Wait()

	if hasKind(logS.snapshot(), transfer.EventFileStart) {
		t.Error("sender emitted file-start despite reject — bytes were transferred")
	}
	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("destination dir not empty after reject: %v", entries)
	}
}

// TestSubsetAccept exercises the accept-with-subset wire path: the receiver
// picks only the second of two files.
func TestSubsetAccept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	a, _ := writeRandom(t, srcDir, "a.bin", 4096)
	b, wantSumB := writeRandom(t, srcDir, "b.bin", 8192)

	snd, rcv := connect(t, ctx)
	files, err := transfer.FilesFromPaths([]string{a, b})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}

	evS := make(chan transfer.Event, 256)
	evR := make(chan transfer.Event, 256)
	go func() {
		for range evS { //nolint:revive // discard
		}
	}()
	go func() {
		for range evR { //nolint:revive // discard
		}
	}()

	onlySecond := func(metas []protocol.FileMeta) ([]int, error) {
		if len(metas) != 2 {
			return nil, fmt.Errorf("manifest has %d files, want 2", len(metas))
		}
		return []int{1}, nil
	}

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	go func() { sendErr <- transfer.Send(ctx, snd, files, evS) }()
	go func() { recvErr <- transfer.Receive(ctx, rcv, dstDir, evR, transfer.WithDecide(onlySecond)) }()

	if err := <-sendErr; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-recvErr; err != nil {
		t.Fatalf("Receive: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dstDir, "a.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a.bin present (stat err %v), want only b.bin", err)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "b.bin"))
	if err != nil {
		t.Fatalf("read b.bin: %v", err)
	}
	if sum := sha256.Sum256(got); sum != wantSumB {
		t.Fatal("b.bin SHA-256 differs from source")
	}
}

// TestCollisionSuffix: two accepted files with the same wire name must land
// as name and name-1 per TP/1 rule 1.
func TestCollisionSuffix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dirA := t.TempDir()
	dirB := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "same.bin"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "same.bin"), []byte("bbbbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	snd, rcv := connect(t, ctx)
	files, err := transfer.FilesFromPaths([]string{filepath.Join(dirA, "same.bin"), filepath.Join(dirB, "same.bin")})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}

	evS := make(chan transfer.Event, 256)
	evR := make(chan transfer.Event, 256)
	go func() {
		for range evS { //nolint:revive // discard
		}
	}()
	go func() {
		for range evR { //nolint:revive // discard
		}
	}()

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	go func() { sendErr <- transfer.Send(ctx, snd, files, evS) }()
	go func() { recvErr <- transfer.Receive(ctx, rcv, dstDir, evR) }()

	if err := <-sendErr; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-recvErr; err != nil {
		t.Fatalf("Receive: %v", err)
	}

	// Ascending IDs: the first file claims the bare name, the second gets -1.
	first, err := os.ReadFile(filepath.Join(dstDir, "same.bin"))
	if err != nil {
		t.Fatalf("read same.bin: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dstDir, "same.bin-1"))
	if err != nil {
		t.Fatalf("read same.bin-1: %v", err)
	}
	if string(first) != "aaa" || string(second) != "bbbbb" {
		t.Fatalf("collision contents: got %q and %q, want %q and %q", first, second, "aaa", "bbbbb")
	}
}
