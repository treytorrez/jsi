package peer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

const (
	// ChannelLabel is the data-channel label required by TP/1
	// (proto/TRANSFER.md): one ordered+reliable channel named "jsi".
	ChannelLabel = "jsi"

	// MaxChunkSize is the largest message WriteFlow accepts: 16 KiB, the
	// RFC 8831 §6.6 conservative interop size (D9, proto/TRANSFER.md).
	// Callers chunk larger payloads themselves.
	MaxChunkSize = 16 * 1024

	// D9 flow-control watermarks: WriteFlow pauses when the SCTP send
	// buffer reaches bufferedHighWatermark and resumes when it drains to
	// bufferedLowWatermark (the values from the official Pion example,
	// portable to browsers).
	bufferedHighWatermark = 1024 * 1024 // 1 MiB
	bufferedLowWatermark  = 512 * 1024  // 512 KiB
)

// Config controls PeerConnection setup.
type Config struct {
	// ICEServers is the STUN/TURN list (typically fetched from the worker's
	// GET /v1/ice per D6, or STUN-only per the D15 policy). Nil or empty
	// means host candidates only.
	ICEServers []webrtc.ICEServer

	// EnableMDNS controls mDNS host candidates (D14). Explicit field
	// semantics: the zero value is false so tests and LAN-only setups get
	// plain host candidates; production callers should use DefaultConfig
	// (or set the field true) for browser parity — mDNS hides LAN IPs from
	// the SDP a remote peer sees.
	EnableMDNS bool
}

// DefaultConfig returns the production default: mDNS enabled per D14, no
// ICE servers (the caller adds STUN/TURN per the D15 externals policy).
func DefaultConfig() Config {
	return Config{EnableMDNS: true}
}

// Conn is one end of a JSI peer connection: a PeerConnection plus the
// single ordered+reliable "jsi" data channel.
type Conn struct {
	pc *webrtc.PeerConnection

	mu sync.Mutex // guards dc
	dc *webrtc.DataChannel

	msgMu      sync.Mutex // guards msgHandler, msgBuf, msgFlushed
	msgHandler func(webrtc.DataChannelMessage)
	msgBuf     []webrtc.DataChannelMessage
	msgFlushed bool

	openCh chan struct{} // cap 1: signaled on channel OnOpen
	lowCh  chan struct{} // cap 1: signaled on OnBufferedAmountLow
}

func newConn(pc *webrtc.PeerConnection) *Conn {
	return &Conn{
		pc:     pc,
		openCh: make(chan struct{}, 1),
		lowCh:  make(chan struct{}, 1),
	}
}

// signal records a one-shot event on a cap-1 channel: an early event is
// buffered for a later waiter, and a pending token is never duplicated.
func signal(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// setChannel wires the flow-control and open handlers and stores dc. It is
// safe to call from Pion callbacks and must run before the channel can
// open (Offer: before SetLocalDescription; Answer: inside OnDataChannel).
// Pion invokes a newly registered OnOpen handler immediately if the
// channel is already open, so an early open is never missed.
func (c *Conn) setChannel(dc *webrtc.DataChannel) {
	dc.SetBufferedAmountLowThreshold(bufferedLowWatermark)
	dc.OnBufferedAmountLow(func() { signal(c.lowCh) })
	dc.OnOpen(func() { signal(c.openCh) })
	// Buffer inbound messages from the moment the channel exists: Pion drops
	// messages arriving with no registered OnMessage handler, so a handler
	// installed later (e.g. by internal/transfer) would otherwise lose early
	// messages (TP/1 hello) to a race.
	dc.OnMessage(c.routeMessage)

	c.mu.Lock()
	c.dc = dc
	c.mu.Unlock()
}

func newPeerConnection(cfg Config) (*webrtc.PeerConnection, error) {
	var se webrtc.SettingEngine
	if cfg.EnableMDNS {
		// D14: browser-parity mDNS host candidates. QueryAndGather is the
		// "enabled" mode in pion/ice/v4: local host candidates are
		// advertised as mDNS names and remote mDNS candidates are resolved.
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeQueryAndGather)
	}
	// PLAN.md §7.2: 0 keeps Pion's default max SCTP message size (stated
	// explicitly for the spec); certificate fingerprint verification is
	// never disabled.
	se.SetSCTPMaxMessageSize(0)

	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).
		NewPeerConnection(webrtc.Configuration{ICEServers: cfg.ICEServers})
	if err != nil {
		return nil, fmt.Errorf("peer: create peer connection: %w", err)
	}
	return pc, nil
}

// waitGather blocks until ICE gathering completes (D2 non-trickle) or ctx
// is done.
func waitGather(ctx context.Context, gather <-chan struct{}) error {
	select {
	case <-gather:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// localDescription returns the fully-gathered local description, i.e. the
// SDP with all a=candidate lines embedded.
func localDescription(pc *webrtc.PeerConnection) (webrtc.SessionDescription, error) {
	ld := pc.LocalDescription()
	if ld == nil {
		return webrtc.SessionDescription{},
			errors.New("peer: local description unavailable after ICE gathering")
	}
	return *ld, nil
}

// Offer creates the offerer side: a PeerConnection, the "jsi" data channel
// (ordered+reliable defaults, created first so the offer carries an
// m=application section), and a fully-gathered non-trickle offer (D2).
// The returned Conn needs SetRemote(answer) before WaitOpen resolves.
func Offer(ctx context.Context, cfg Config) (*Conn, webrtc.SessionDescription, error) {
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return nil, webrtc.SessionDescription{}, err
	}
	c := newConn(pc)
	fail := func(err error) (*Conn, webrtc.SessionDescription, error) {
		_ = pc.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	// Data channel FIRST: nil options = ordered+reliable (browser parity,
	// D9), and the offer must contain an m=application section.
	dc, err := pc.CreateDataChannel(ChannelLabel, nil)
	if err != nil {
		return fail(fmt.Errorf("peer: create data channel: %w", err))
	}
	c.setChannel(dc)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fail(fmt.Errorf("peer: create offer: %w", err))
	}

	// D2 non-trickle: the promise is created BEFORE SetLocalDescription.
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return fail(fmt.Errorf("peer: set local description: %w", err))
	}
	if err := waitGather(ctx, gather); err != nil {
		return fail(err)
	}

	desc, err := localDescription(pc)
	if err != nil {
		return fail(err)
	}
	return c, desc, nil
}

// Answer creates the answerer side from a fully-gathered offer: it
// captures the inbound "jsi" channel via OnDataChannel, applies the offer,
// and returns a fully-gathered non-trickle answer (D2).
func Answer(ctx context.Context, cfg Config, offer webrtc.SessionDescription) (*Conn, webrtc.SessionDescription, error) {
	pc, err := newPeerConnection(cfg)
	if err != nil {
		return nil, webrtc.SessionDescription{}, err
	}
	c := newConn(pc)
	fail := func(err error) (*Conn, webrtc.SessionDescription, error) {
		_ = pc.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	// Registered before SetRemoteDescription so the inbound channel cannot
	// be missed.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { c.setChannel(dc) })

	if err := pc.SetRemoteDescription(offer); err != nil {
		return fail(fmt.Errorf("peer: set remote description: %w", err))
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(fmt.Errorf("peer: create answer: %w", err))
	}

	// D2 non-trickle: the promise is created BEFORE SetLocalDescription.
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(fmt.Errorf("peer: set local description: %w", err))
	}
	if err := waitGather(ctx, gather); err != nil {
		return fail(err)
	}

	desc, err := localDescription(pc)
	if err != nil {
		return fail(err)
	}
	return c, desc, nil
}

// SetRemote installs the remote description (the answer, on the offerer
// side). The context is accepted for symmetry with Offer/Answer; Pion's
// SetRemoteDescription is non-blocking and takes no context.
func (c *Conn) SetRemote(_ context.Context, desc webrtc.SessionDescription) error {
	if err := c.pc.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("peer: set remote description: %w", err)
	}
	return nil
}

// WaitOpen resolves when the data channel fires OnOpen (the created
// channel on the offerer, the captured inbound channel on the answerer).
// It does not resolve on Close; ctx cancellation unblocks it.
func (c *Conn) WaitOpen(ctx context.Context) error {
	select {
	case <-c.openCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Channel returns the data channel. Valid (non-nil) after WaitOpen; may be
// nil earlier, e.g. on the answerer before the remote offer is applied.
func (c *Conn) Channel() *webrtc.DataChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dc
}

// routeMessage buffers inbound messages until OnMessage installs a handler.
func (c *Conn) routeMessage(m webrtc.DataChannelMessage) {
	c.msgMu.Lock()
	if c.msgFlushed {
		h := c.msgHandler
		c.msgMu.Unlock()
		if h != nil {
			h(m)
		}
		return
	}
	c.msgBuf = append(c.msgBuf, m)
	c.msgMu.Unlock()
}

// OnMessage sets the message handler, replaying any messages buffered before
// the call in arrival order first. Messages arriving during replay keep
// buffering and are drained by the replay loop, so ordering is preserved.
// Subsequent calls replace the handler.
func (c *Conn) OnMessage(f func(webrtc.DataChannelMessage)) {
	c.msgMu.Lock()
	c.msgHandler = f
	c.msgMu.Unlock()
	for {
		c.msgMu.Lock()
		if len(c.msgBuf) == 0 {
			c.msgFlushed = true
			c.msgMu.Unlock()
			return
		}
		buf := c.msgBuf
		c.msgBuf = nil
		c.msgMu.Unlock()
		for _, m := range buf {
			f(m)
		}
	}
}

// WriteFlow sends one chunk with D9 flow control: when the buffered amount
// reaches 1 MiB it waits for OnBufferedAmountLow (threshold 512 KiB, set
// once at channel setup) or ctx cancellation, then sends. data must be at
// most MaxChunkSize; the caller chunks larger payloads (16 KiB per TP/1).
func (c *Conn) WriteFlow(ctx context.Context, data []byte) error {
	if len(data) > MaxChunkSize {
		return fmt.Errorf("peer: chunk size %d exceeds MaxChunkSize %d", len(data), MaxChunkSize)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dc := c.Channel()
	if dc == nil {
		return errors.New("peer: data channel not ready (call WaitOpen first)")
	}
	for dc.BufferedAmount() >= bufferedHighWatermark {
		select {
		case <-c.lowCh:
			// Re-check BufferedAmount: the token may be stale.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := dc.Send(data); err != nil {
		return fmt.Errorf("peer: send: %w", err)
	}
	return nil
}

// ConnectionPath reports how the established connection carries bytes, for
// the D15 transparency line printed at connect time ("connected: …"). It
// reads the nominated ICE candidate pair from PeerConnection stats and maps
// the local candidate type to a short human string:
//
//	host  → "direct (host)"
//	srflx → "via STUN (srflx)"
//	prflx → "via peer-reflexive (prflx)"
//	relay → "TURN RELAY (Cloudflare carries encrypted bytes)"
//
// (v1 TURN is Cloudflare Realtime, minted by the worker per D7; a relay sees
// DTLS ciphertext only, PLAN.md §10.) Call after WaitOpen. The ctx is
// checked before the (synchronous) stats read.
func (c *Conn) ConnectionPath(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	report := c.pc.GetStats()
	var pair *webrtc.ICECandidatePairStats
	for _, s := range report {
		if ps, ok := s.(webrtc.ICECandidatePairStats); ok &&
			ps.Nominated && ps.State == webrtc.StatsICECandidatePairStateSucceeded {
			p := ps
			pair = &p
			break
		}
	}
	if pair == nil {
		return "", errors.New("peer: no nominated ICE candidate pair (not connected?)")
	}
	local, ok := report[pair.LocalCandidateID].(webrtc.ICECandidateStats)
	if !ok {
		return "", fmt.Errorf("peer: local candidate %q missing from stats report", pair.LocalCandidateID)
	}
	switch local.CandidateType {
	case webrtc.ICECandidateTypeHost:
		return "direct (host)", nil
	case webrtc.ICECandidateTypeSrflx:
		return "via STUN (srflx)", nil
	case webrtc.ICECandidateTypePrflx:
		return "via peer-reflexive (prflx)", nil
	case webrtc.ICECandidateTypeRelay:
		return "TURN RELAY (Cloudflare carries encrypted bytes)", nil
	default:
		return "", fmt.Errorf("peer: unknown local candidate type %v", local.CandidateType)
	}
}

// Close closes the data channel and the PeerConnection.
func (c *Conn) Close() error {
	c.mu.Lock()
	dc := c.dc
	c.mu.Unlock()

	var err error
	if dc != nil {
		err = dc.Close()
	}
	if cerr := c.pc.Close(); err == nil {
		err = cerr
	}
	return err
}
