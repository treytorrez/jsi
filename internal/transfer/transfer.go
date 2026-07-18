package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/protocol"
)

// appName is sent in Hello.App (optional per TP/1; identifies this stack).
const appName = "jsi-go"

// maxCollisionAttempts bounds the "-1", "-2", … collision suffix search
// (TP/1 rule 1) before giving up.
const maxCollisionAttempts = 1000

// File is one file to send. Path is the local filesystem path to read;
// Name is the wire name carried in the manifest (a sanitized base name,
// see FilesFromPaths); Size is the declared size in bytes.
type File struct {
	Path string
	Name string
	Size int64
}

// FilesFromPaths stats paths and builds the send list: Name is the base
// name run through protocol.SanitizeName, Size from the filesystem.
// Missing paths, directories, non-regular files (devices, FIFOs, …), and
// names SanitizeName cannot make wire-safe are rejected.
func FilesFromPaths(paths []string) ([]File, error) {
	files := make([]File, 0, len(paths))
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("transfer: stat %s: %w", p, err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("transfer: %s is a directory", p)
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("transfer: %s is not a regular file", p)
		}
		name, err := protocol.SanitizeName(filepath.Base(p))
		if err != nil {
			return nil, fmt.Errorf("transfer: %s: %w", p, err)
		}
		files = append(files, File{Path: p, Name: name, Size: st.Size()})
	}
	return files, nil
}

// EventKind identifies what an Event reports.
type EventKind int

const (
	// EventFileStart begins one file: FileID set, BytesTotal = declared size.
	EventFileStart EventKind = iota
	// EventProgress reports one chunk moved: BytesDone is cumulative for
	// FileID, BytesTotal its declared size.
	EventProgress
	// EventFileDone completes one file: BytesDone == BytesTotal. Err is nil
	// on success, or wraps ErrHashMismatch when verification failed (the
	// receiver has already deleted the corrupt file in that case).
	EventFileDone
	// EventDone ends the session successfully. It is the terminal event.
	EventDone
	// EventError ends the session with a failure: Err is the error Send or
	// Receive is about to return. It is the terminal event.
	EventError
)

// String renders the kind for logs and tests.
func (k EventKind) String() string {
	switch k {
	case EventFileStart:
		return "file-start"
	case EventProgress:
		return "progress"
	case EventFileDone:
		return "file-done"
	case EventDone:
		return "done"
	case EventError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is one progress notification from Send or Receive.
//
// Event contract: per accepted file, exactly one EventFileStart, zero or
// more EventProgress, and exactly one EventFileDone. The session then ends
// with exactly one terminal event — EventDone iff the call returns nil,
// otherwise EventError carrying the returned error — after which ev is
// closed. FileID, BytesDone, and BytesTotal are meaningful only for the
// per-file kinds.
type Event struct {
	Kind       EventKind
	FileID     int
	BytesDone  int64
	BytesTotal int64
	Err        error
}

// Endpoint is the seam Send and Receive need from a peer connection: the
// open "jsi" data channel plus flow-controlled binary writes. D9 flow
// control (512 KiB/1 MiB watermarks) lives in the endpoint's WriteFlow —
// (*peer.Conn satisfies Endpoint) — so this package stays a pure state
// machine.
type Endpoint interface {
	Channel() *webrtc.DataChannel
	WriteFlow(ctx context.Context, data []byte) error
}

// Typed errors returned by Send and Receive (wrap with fmt.Errorf "%w";
// match with errors.Is).
var (
	// ErrRejected: the peer rejected the transfer (sender side).
	ErrRejected = errors.New("transfer: rejected by peer")
	// ErrDeclined: the local Decide function accepted no files (receiver side).
	ErrDeclined = errors.New("transfer: declined, no files accepted")
	// ErrHashMismatch: streamed SHA-256 did not match the file-end digest.
	ErrHashMismatch = errors.New("transfer: sha-256 mismatch")
	// ErrPeerCancel: the peer sent a cancel control message.
	ErrPeerCancel = errors.New("transfer: canceled by peer")
	// ErrPeerClosed: the data channel closed unexpectedly.
	ErrPeerClosed = errors.New("transfer: data channel closed by peer")
	// ErrProtocol: the peer violated TP/1; an error control message was sent.
	ErrProtocol = errors.New("transfer: protocol violation")
	// ErrVersion: the peer's hello carried an unsupported TP version.
	ErrVersion = errors.New("transfer: unsupported TP version")
)

// PeerError is an error control message received from the peer.
type PeerError struct {
	Code    string // protocol.CodeProtocol or protocol.CodeVersion
	Message string
}

// Error implements error.
func (e *PeerError) Error() string {
	return fmt.Sprintf("transfer: peer error %q: %s", e.Code, e.Message)
}

// Option configures Receive.
type Option func(*receiveConfig)

type receiveConfig struct {
	decide func([]protocol.FileMeta) ([]int, error)
}

// WithDecide sets the function that chooses which manifest files to accept.
// It is called once per session with the received manifest and returns the
// accepted file IDs (any order; files transfer ascending). An empty result
// rejects the transfer (Receive returns ErrDeclined); a non-nil error
// rejects it with the error as the reason. Default: accept all files.
func WithDecide(fn func([]protocol.FileMeta) ([]int, error)) Option {
	return func(c *receiveConfig) { c.decide = fn }
}

func acceptAll(files []protocol.FileMeta) ([]int, error) {
	ids := make([]int, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	return ids, nil
}

// emit delivers e to ev. A nil ev drops events; a canceled ctx may drop the
// event rather than block a session that is ending anyway.
func emit(ctx context.Context, ev chan<- Event, e Event) {
	if ev == nil {
		return
	}
	select {
	case ev <- e:
	case <-ctx.Done():
	}
}

// session owns the channel interaction for one Send or Receive run: an
// inbox fed by OnMessage, close tracking, and control-message sending.
type session struct {
	dc    *webrtc.DataChannel
	inbox chan webrtc.DataChannelMessage

	done      chan struct{} // closed by end: OnMessage starts dropping
	endOnce   sync.Once
	closed    chan struct{} // closed on channel OnClose
	closeOnce sync.Once

	pending  []webrtc.DataChannelMessage // messages deferred by poll
	signaled bool                        // terminal control already sent/received
}

// newSession takes over the channel's OnMessage and OnClose handlers.
func newSession(ep Endpoint) *session {
	s := &session{
		dc:     ep.Channel(),
		inbox:  make(chan webrtc.DataChannelMessage, 256),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	s.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		// Blocking on inbox (while the session runs) is the D9 receive-side
		// backpressure; after end, messages drop instead of piling up.
		select {
		case s.inbox <- m:
		case <-s.done:
		}
	})
	s.dc.OnClose(func() { s.closeOnce.Do(func() { close(s.closed) }) })
	return s
}

// end stops the message pump; called exactly once via defer.
func (s *session) end() { s.endOnce.Do(func() { close(s.done) }) }

// next returns the next inbound message, honoring ctx, deferred messages,
// and channel close.
func (s *session) next(ctx context.Context) (webrtc.DataChannelMessage, error) {
	if len(s.pending) > 0 {
		m := s.pending[0]
		s.pending = s.pending[1:]
		return m, nil
	}
	select {
	case m := <-s.inbox:
		return m, nil
	case <-s.closed:
		// The channel is ordered and reliable: anything the peer sent
		// before closing is already queued — drain it before reporting
		// the close.
		select {
		case m := <-s.inbox:
			return m, nil
		default:
			return webrtc.DataChannelMessage{}, ErrPeerClosed
		}
	case <-ctx.Done():
		return webrtc.DataChannelMessage{}, ctx.Err()
	}
}

// poll returns a pending or already-queued message without blocking.
func (s *session) poll() (webrtc.DataChannelMessage, bool) {
	if len(s.pending) > 0 {
		m := s.pending[0]
		s.pending = s.pending[1:]
		return m, true
	}
	select {
	case m := <-s.inbox:
		return m, true
	default:
		return webrtc.DataChannelMessage{}, false
	}
}

// pushback defers m to the next next/poll call.
func (s *session) pushback(m webrtc.DataChannelMessage) { s.pending = append(s.pending, m) }

// sendControl marshals and sends one TP/1 control message as channel text.
func (s *session) sendControl(v any) error {
	data, err := protocol.Marshal(v)
	if err != nil {
		return err
	}
	if err := s.dc.SendText(string(data)); err != nil {
		return fmt.Errorf("transfer: send %s: %w", typeOf(v), err)
	}
	return nil
}

// protocolError reports a TP/1 violation: it sends an error control message
// (best effort) and returns a typed ErrProtocol for the caller to return.
func (s *session) protocolError(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if s.sendControl(protocol.Error{Code: protocol.CodeProtocol, Message: msg}) == nil {
		s.signaled = true
	}
	return fmt.Errorf("%w: %s", ErrProtocol, msg)
}

// terminal checks whether msg is a cancel or error control message — the
// two messages any wait loop must honor. The session is marked signaled so
// no redundant cancel goes out on the way back.
func (s *session) terminal(msg any) (error, bool) {
	switch v := msg.(type) {
	case protocol.Cancel:
		s.signaled = true
		if v.Reason == "" {
			return ErrPeerCancel, true
		}
		return fmt.Errorf("%w: %s", ErrPeerCancel, v.Reason), true
	case protocol.Error:
		s.signaled = true
		return &PeerError{Code: v.Code, Message: v.Message}, true
	default:
		return nil, false
	}
}

// abort best-effort signals the peer that the session ends due to a local
// failure (ctx cancellation or local I/O error), unless a terminal control
// message was already exchanged either way.
func (s *session) abort(reason string) {
	if s.signaled {
		return
	}
	_ = s.sendControl(protocol.Cancel{Reason: reason})
}

// handshake exchanges hello messages: ours goes out, then the peer's first
// message must be a hello with V == protocol.Version (TP/1 §Versioning).
func (s *session) handshake(ctx context.Context, app string) error {
	if err := s.sendControl(protocol.Hello{V: protocol.Version, App: app}); err != nil {
		return err
	}
	for {
		m, err := s.next(ctx)
		if err != nil {
			return err
		}
		if !m.IsString {
			return s.protocolError("expected hello, got binary chunk")
		}
		msg, err := decodeText(m)
		if err != nil {
			return s.protocolError("%v", err)
		}
		if terr, ok := s.terminal(msg); ok {
			return terr
		}
		switch v := msg.(type) {
		case protocol.Hello:
			if v.V != protocol.Version {
				if s.sendControl(protocol.Error{
					Code:    protocol.CodeVersion,
					Message: "unsupported TP version",
				}) == nil {
					s.signaled = true
				}
				return fmt.Errorf("%w: peer sent v=%d", ErrVersion, v.V)
			}
			return nil
		default:
			return s.protocolError("expected hello, got %s", typeOf(msg))
		}
	}
}

// decodeText JSON-decodes one text message into its TP/1 type.
func decodeText(m webrtc.DataChannelMessage) (any, error) {
	msg, err := protocol.Unmarshal(m.Data)
	if err != nil {
		return nil, fmt.Errorf("malformed control message: %w", err)
	}
	return msg, nil
}

// typeOf names a decoded control message for error text.
func typeOf(msg any) string {
	switch m := msg.(type) {
	case protocol.Hello:
		return protocol.TypeHello
	case protocol.Manifest:
		return protocol.TypeManifest
	case protocol.Accept:
		return protocol.TypeAccept
	case protocol.Reject:
		return protocol.TypeReject
	case protocol.FileStart:
		return protocol.TypeFileStart
	case protocol.FileEnd:
		return protocol.TypeFileEnd
	case protocol.FileAck:
		return protocol.TypeFileAck
	case protocol.Done:
		return protocol.TypeDone
	case protocol.Cancel:
		return protocol.TypeCancel
	case protocol.Error:
		return protocol.TypeError
	case protocol.Unknown:
		return m.Type
	default:
		return fmt.Sprintf("%T", msg)
	}
}

// Send runs the TP/1 sender state machine (proto/TRANSFER.md) over ep:
// hello → manifest → accept/reject → per accepted file ascending ID
// (file-start → ≤16 KiB chunks → file-end with streamed SHA-256 → file-ack)
// → done. Progress is reported on ev (see the Event contract); Send closes
// ev just before returning.
//
// The channel must be open before Send is called (peer.Conn.WaitOpen), and
// Send must be called promptly: it installs its own OnMessage/OnClose
// handlers, and Pion drops messages that arrive before a handler exists —
// the caller must not touch channel handlers or use the channel for
// anything else until Send returns. File bytes go exclusively through
// ep.WriteFlow (D9 watermarks); control messages go through SendText.
//
// There are no built-in timeouts: ctx is the only deadline. On ctx
// cancellation Send attempts a cancel control message, then returns
// ctx.Err(). A hash-mismatch file-ack is a failure: after emitting
// EventFileDone with the error, Send aborts with a typed ErrHashMismatch.
func Send(ctx context.Context, ep Endpoint, files []File, ev chan<- Event) (retErr error) {
	defer func() {
		if ev != nil {
			close(ev)
		}
	}()
	fail := func(err error) error {
		emit(ctx, ev, Event{Kind: EventError, Err: err})
		return err
	}

	if ep.Channel() == nil {
		return fail(errors.New("transfer: data channel not open (call WaitOpen first)"))
	}
	if len(files) == 0 {
		return fail(errors.New("transfer: no files to send"))
	}
	if len(files) > protocol.MaxFiles {
		return fail(fmt.Errorf("transfer: %d files exceeds manifest max %d", len(files), protocol.MaxFiles))
	}
	metas := make([]protocol.FileMeta, len(files))
	for i, f := range files {
		name, err := protocol.SanitizeName(f.Name)
		if err != nil {
			return fail(fmt.Errorf("transfer: file %d (%s): %w", i, f.Path, err))
		}
		m := protocol.FileMeta{ID: i, Name: name, Size: f.Size}
		if err := protocol.Validate(m); err != nil {
			return fail(err)
		}
		metas[i] = m
	}

	s := newSession(ep)
	defer s.end()
	defer func() {
		if retErr != nil {
			reason := "sender aborted"
			if ctx.Err() != nil {
				reason = ctx.Err().Error()
			}
			s.abort(reason)
		}
	}()

	if err := s.handshake(ctx, appName); err != nil {
		return fail(err)
	}
	if err := s.sendControl(protocol.Manifest{Files: metas}); err != nil {
		return fail(err)
	}

	var accepted []int
waitReply:
	for {
		m, err := s.next(ctx)
		if err != nil {
			return fail(err)
		}
		if !m.IsString {
			return fail(s.protocolError("binary chunk before accept"))
		}
		msg, err := decodeText(m)
		if err != nil {
			return fail(s.protocolError("%v", err))
		}
		if terr, ok := s.terminal(msg); ok {
			return fail(terr)
		}
		switch v := msg.(type) {
		case protocol.Unknown:
			continue // TP/1 rule 5: ignore unknown types
		case protocol.Reject:
			if v.Reason == "" {
				return fail(ErrRejected)
			}
			return fail(fmt.Errorf("%w: %s", ErrRejected, v.Reason))
		case protocol.Accept:
			accepted, err = resolveAccept(v, len(files))
			if err != nil {
				return fail(s.protocolError("%v", err))
			}
			break waitReply
		default:
			return fail(s.protocolError("expected accept or reject, got %s", typeOf(msg)))
		}
	}

	for _, id := range accepted {
		if err := s.sendFile(ctx, ep, files[id], metas[id], ev); err != nil {
			return fail(err)
		}
	}
	if err := s.sendControl(protocol.Done{}); err != nil {
		return fail(err)
	}
	emit(ctx, ev, Event{Kind: EventDone})
	return nil
}

// resolveAccept maps an accept message to the ascending list of file IDs to
// send: empty Files means the whole manifest.
func resolveAccept(a protocol.Accept, n int) ([]int, error) {
	if len(a.Files) == 0 {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	seen := make(map[int]bool, len(a.Files))
	ids := make([]int, 0, len(a.Files))
	for _, id := range a.Files {
		if id < 0 || id >= n {
			return nil, fmt.Errorf("accept id %d out of range 0..%d", id, n-1)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// sendFile transfers one accepted file and waits for its ack.
func (s *session) sendFile(ctx context.Context, ep Endpoint, f File, meta protocol.FileMeta, ev chan<- Event) error {
	if err := s.sendControl(protocol.FileStart{ID: meta.ID}); err != nil {
		return err
	}
	emit(ctx, ev, Event{Kind: EventFileStart, FileID: meta.ID, BytesTotal: meta.Size})

	fh, err := os.Open(f.Path)
	if err != nil {
		return fmt.Errorf("transfer: open %s: %w", f.Path, err)
	}
	defer func() { _ = fh.Close() }()

	// Stream the file in ≤16 KiB chunks, hashing while reading (D10).
	h := sha256.New()
	tee := io.TeeReader(fh, h)
	buf := make([]byte, protocol.ChunkSize)
	var sent int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// React promptly to a peer cancel/error without stopping the stream.
		if m, ok := s.poll(); ok {
			if err := s.midSendCheck(m); err != nil {
				return err
			}
		}
		n, rerr := tee.Read(buf)
		if n > 0 {
			if err := ep.WriteFlow(ctx, buf[:n]); err != nil {
				return fmt.Errorf("transfer: file %d: %w", meta.ID, err)
			}
			sent += int64(n)
			emit(ctx, ev, Event{Kind: EventProgress, FileID: meta.ID, BytesDone: sent, BytesTotal: meta.Size})
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("transfer: read %s: %w", f.Path, rerr)
		}
	}
	if sent != meta.Size {
		return fmt.Errorf("transfer: %s changed size during send: read %d of %d declared bytes", f.Path, sent, meta.Size)
	}

	if err := s.sendControl(protocol.FileEnd{ID: meta.ID, SHA256: hex.EncodeToString(h.Sum(nil))}); err != nil {
		return err
	}
	ack, err := s.waitAck(ctx, meta.ID)
	if err != nil {
		return err
	}
	switch ack.Status {
	case protocol.StatusOK:
		emit(ctx, ev, Event{Kind: EventFileDone, FileID: meta.ID, BytesDone: sent, BytesTotal: meta.Size})
		return nil
	case protocol.StatusHashMismatch:
		err := fmt.Errorf("%w: file %d (%s): receiver reports %d bytes", ErrHashMismatch, meta.ID, meta.Name, ack.Bytes)
		emit(ctx, ev, Event{Kind: EventFileDone, FileID: meta.ID, BytesDone: sent, BytesTotal: meta.Size, Err: err})
		return err
	default:
		return s.protocolError("file %d: unknown file-ack status %q", meta.ID, ack.Status)
	}
}

// midSendCheck handles one message dequeued mid-stream: cancel/error end
// the session, unknown types are ignored (rule 5), anything else is
// deferred to the wait loop that expects it.
func (s *session) midSendCheck(m webrtc.DataChannelMessage) error {
	if !m.IsString {
		return s.protocolError("unexpected binary chunk from receiver")
	}
	msg, err := decodeText(m)
	if err != nil {
		return s.protocolError("%v", err)
	}
	if terr, ok := s.terminal(msg); ok {
		return terr
	}
	if _, unknown := msg.(protocol.Unknown); !unknown {
		s.pushback(m)
	}
	return nil
}

// waitAck waits for the file-ack for id.
func (s *session) waitAck(ctx context.Context, id int) (protocol.FileAck, error) {
	for {
		m, err := s.next(ctx)
		if err != nil {
			return protocol.FileAck{}, err
		}
		if !m.IsString {
			return protocol.FileAck{}, s.protocolError("unexpected binary chunk from receiver")
		}
		msg, err := decodeText(m)
		if err != nil {
			return protocol.FileAck{}, s.protocolError("%v", err)
		}
		if terr, ok := s.terminal(msg); ok {
			return protocol.FileAck{}, terr
		}
		switch v := msg.(type) {
		case protocol.Unknown:
			continue
		case protocol.FileAck:
			if v.ID != id {
				return protocol.FileAck{}, s.protocolError("expected file-ack id %d, got %d", id, v.ID)
			}
			return v, nil
		default:
			return protocol.FileAck{}, s.protocolError("expected file-ack, got %s", typeOf(msg))
		}
	}
}

// Receive runs the TP/1 receiver state machine (proto/TRANSFER.md) over ep:
// hello → manifest → accept (all files, or the WithDecide subset) / reject →
// per accepted file ascending ID (file-start → chunks counted against the
// declared size → file-end with hash verification → file-ack) → done.
// Files are written into destDir (created if missing) under their sanitized
// names; collisions get "-1", "-2", … suffixes (TP/1 rule 1). Progress is
// reported on ev (see the Event contract); Receive closes ev just before
// returning.
//
// The channel must be open before Receive is called (peer.Conn.WaitOpen),
// and Receive must be called promptly: it installs its own OnMessage/OnClose
// handlers, and Pion drops messages that arrive before a handler exists —
// the caller must not touch channel handlers or use the channel for
// anything else until Receive returns.
//
// There are no built-in timeouts: ctx is the only deadline. On ctx
// cancellation Receive attempts a cancel control message, then returns
// ctx.Err(). Protocol violations send an error control message and return a
// typed ErrProtocol. A hash mismatch deletes the file, acks hash-mismatch,
// emits EventFileDone with the error, and fails with a typed
// ErrHashMismatch. Partial files are removed on any failure.
func Receive(ctx context.Context, ep Endpoint, destDir string, ev chan<- Event, opts ...Option) (retErr error) {
	defer func() {
		if ev != nil {
			close(ev)
		}
	}()
	fail := func(err error) error {
		emit(ctx, ev, Event{Kind: EventError, Err: err})
		return err
	}

	if ep.Channel() == nil {
		return fail(errors.New("transfer: data channel not open (call WaitOpen first)"))
	}
	cfg := receiveConfig{decide: acceptAll}
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fail(fmt.Errorf("transfer: create destination dir: %w", err))
	}

	s := newSession(ep)
	defer s.end()
	defer func() {
		if retErr != nil {
			reason := "receiver aborted"
			if ctx.Err() != nil {
				reason = ctx.Err().Error()
			}
			s.abort(reason)
		}
	}()

	if err := s.handshake(ctx, appName); err != nil {
		return fail(err)
	}

	var manifest protocol.Manifest
waitManifest:
	for {
		m, err := s.next(ctx)
		if err != nil {
			return fail(err)
		}
		if !m.IsString {
			return fail(s.protocolError("binary chunk before manifest"))
		}
		msg, err := decodeText(m)
		if err != nil {
			return fail(s.protocolError("%v", err))
		}
		if terr, ok := s.terminal(msg); ok {
			return fail(terr)
		}
		switch v := msg.(type) {
		case protocol.Unknown:
			continue // TP/1 rule 5: ignore unknown types
		case protocol.Manifest:
			manifest = v
			break waitManifest
		default:
			return fail(s.protocolError("expected manifest, got %s", typeOf(msg)))
		}
	}

	if err := validateManifest(manifest); err != nil {
		return fail(s.protocolError("invalid manifest: %v", err))
	}
	byID := make(map[int]protocol.FileMeta, len(manifest.Files))
	for _, fm := range manifest.Files {
		byID[fm.ID] = fm
	}

	ids, err := cfg.decide(manifest.Files)
	if err != nil {
		if s.sendControl(protocol.Reject{Reason: err.Error()}) == nil {
			s.signaled = true
		}
		return fail(fmt.Errorf("transfer: decide: %w", err))
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if _, ok := byID[id]; !ok || seen[id] {
			if s.sendControl(protocol.Reject{Reason: "invalid acceptance"}) == nil {
				s.signaled = true
			}
			return fail(fmt.Errorf("transfer: decide returned invalid file id %d", id))
		}
		seen[id] = true
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		if s.sendControl(protocol.Reject{Reason: "no files accepted"}) == nil {
			s.signaled = true
		}
		return fail(ErrDeclined)
	}

	accept := protocol.Accept{}
	if len(ids) != len(manifest.Files) {
		accept.Files = ids // subset; empty Files would mean "all"
	}
	if err := s.sendControl(accept); err != nil {
		return fail(err)
	}

	for _, id := range ids {
		if err := s.receiveFile(ctx, destDir, byID[id], ev); err != nil {
			return fail(err)
		}
	}

	for {
		m, err := s.next(ctx)
		if err != nil {
			return fail(err)
		}
		if !m.IsString {
			return fail(s.protocolError("binary chunk after last file"))
		}
		msg, err := decodeText(m)
		if err != nil {
			return fail(s.protocolError("%v", err))
		}
		if terr, ok := s.terminal(msg); ok {
			return fail(terr)
		}
		switch msg.(type) {
		case protocol.Unknown:
			continue
		case protocol.Done:
			emit(ctx, ev, Event{Kind: EventDone})
			return nil
		default:
			return fail(s.protocolError("expected done, got %s", typeOf(msg)))
		}
	}
}

// validateManifest enforces TP/1 rules 1–2 on a received manifest.
func validateManifest(m protocol.Manifest) error {
	if len(m.Files) > protocol.MaxFiles {
		return fmt.Errorf("%d files exceeds max %d", len(m.Files), protocol.MaxFiles)
	}
	seen := make(map[int]bool, len(m.Files))
	for _, f := range m.Files {
		if err := protocol.Validate(f); err != nil {
			return err
		}
		if f.ID < 0 || seen[f.ID] {
			return fmt.Errorf("duplicate or negative file id %d", f.ID)
		}
		seen[f.ID] = true
		if _, err := protocol.SanitizeName(f.Name); err != nil {
			return fmt.Errorf("file %d name: %w", f.ID, err)
		}
	}
	return nil
}

// receiveFile receives one accepted file into destDir and acks it.
func (s *session) receiveFile(ctx context.Context, destDir string, meta protocol.FileMeta, ev chan<- Event) error {
waitStart:
	for {
		m, err := s.next(ctx)
		if err != nil {
			return err
		}
		if !m.IsString {
			return s.protocolError("binary chunk before file-start")
		}
		msg, err := decodeText(m)
		if err != nil {
			return s.protocolError("%v", err)
		}
		if terr, ok := s.terminal(msg); ok {
			return terr
		}
		switch v := msg.(type) {
		case protocol.Unknown:
			continue
		case protocol.FileStart:
			if v.ID != meta.ID {
				return s.protocolError("expected file-start id %d, got %d", meta.ID, v.ID)
			}
			break waitStart
		default:
			return s.protocolError("expected file-start, got %s", typeOf(msg))
		}
	}

	name, err := protocol.SanitizeName(meta.Name) // pre-validated; belt and braces
	if err != nil {
		return s.protocolError("file %d name: %v", meta.ID, err)
	}
	fh, path, err := createDest(destDir, name)
	if err != nil {
		return fmt.Errorf("transfer: create destination: %w", err)
	}
	// Any exit before a verified ok removes the partial file.
	verified := false
	defer func() {
		_ = fh.Close()
		if !verified {
			_ = os.Remove(path)
		}
	}()

	h := sha256.New()
	w := io.MultiWriter(fh, h)
	var got int64
	emit(ctx, ev, Event{Kind: EventFileStart, FileID: meta.ID, BytesTotal: meta.Size})
	for {
		m, err := s.next(ctx)
		if err != nil {
			return err
		}
		if !m.IsString {
			if got+int64(len(m.Data)) > meta.Size {
				return s.protocolError("file %d: chunks exceed declared size %d", meta.ID, meta.Size)
			}
			if _, err := w.Write(m.Data); err != nil {
				return fmt.Errorf("transfer: write %s: %w", path, err)
			}
			got += int64(len(m.Data))
			emit(ctx, ev, Event{Kind: EventProgress, FileID: meta.ID, BytesDone: got, BytesTotal: meta.Size})
			continue
		}
		msg, err := decodeText(m)
		if err != nil {
			return s.protocolError("%v", err)
		}
		if terr, ok := s.terminal(msg); ok {
			return terr
		}
		switch v := msg.(type) {
		case protocol.Unknown:
			continue
		case protocol.FileEnd:
			if v.ID != meta.ID {
				return s.protocolError("expected file-end id %d, got %d", meta.ID, v.ID)
			}
			if got != meta.Size {
				return s.protocolError("file %d: file-end after %d of %d declared bytes", meta.ID, got, meta.Size)
			}
			// The digest compare is deliberately case-insensitive on the
			// hex digits; the digest itself is authoritative either way.
			sum := hex.EncodeToString(h.Sum(nil))
			if !strings.EqualFold(sum, v.SHA256) {
				_ = s.sendControl(protocol.FileAck{ID: meta.ID, Status: protocol.StatusHashMismatch, Bytes: got})
				err := fmt.Errorf("%w: file %d (%s)", ErrHashMismatch, meta.ID, meta.Name)
				emit(ctx, ev, Event{Kind: EventFileDone, FileID: meta.ID, BytesDone: got, BytesTotal: meta.Size, Err: err})
				return err
			}
			if err := fh.Close(); err != nil {
				return fmt.Errorf("transfer: close %s: %w", path, err)
			}
			verified = true
			if err := s.sendControl(protocol.FileAck{ID: meta.ID, Status: protocol.StatusOK, Bytes: got}); err != nil {
				return err
			}
			emit(ctx, ev, Event{Kind: EventFileDone, FileID: meta.ID, BytesDone: got, BytesTotal: meta.Size})
			return nil
		default:
			return s.protocolError("expected file chunks or file-end, got %s", typeOf(msg))
		}
	}
}

// createDest opens name in dir for exclusive creation, trying "-1", "-2",
// … collision suffixes per TP/1 rule 1. Returns the open file and its path.
func createDest(dir, name string) (*os.File, string, error) {
	for i := range maxCollisionAttempts {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", name, i)
		}
		path := filepath.Join(dir, candidate)
		fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return fh, path, nil
	}
	return nil, "", fmt.Errorf("too many name collisions for %q", name)
}
