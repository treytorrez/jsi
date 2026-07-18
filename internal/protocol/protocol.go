package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Version is the TP/1 protocol version, carried in Hello.V. Peers with a
// mismatched version send an Error with CodeVersion and close
// (proto/TRANSFER.md §Versioning).
const Version = 1

// Flow-control and framing constants (proto/TRANSFER.md §Flow control;
// PLAN.md decision D9). ChunkSize bounds one binary data-channel message;
// the watermarks bound DataChannel.BufferedAmount() on the sender.
const (
	ChunkSize              = 16384     // 16 KiB — RFC 8831 §6.6 interop value
	HighWatermarkBytes     = 1 << 20   // pause sending at >= 1 MiB buffered
	LowWatermarkBytes      = 512 << 10 // resume sending at <= 512 KiB buffered
	HardBufferedLimitBytes = 4 << 20   // never let BufferedAmount exceed 4 MiB
)

// Manifest limits (proto/TRANSFER.md rule 2).
const (
	MaxFiles     = 1024             // max entries in Manifest.Files
	MaxNameBytes = 255              // max UTF-8 byte length of FileMeta.Name
	MaxFileSize  = int64(1)<<53 - 1 // max declared file size (JSON-safe integer)
)

// Control message type strings — the "type" field of every text message.
const (
	TypeHello     = "hello"
	TypeManifest  = "manifest"
	TypeAccept    = "accept"
	TypeReject    = "reject"
	TypeFileStart = "file-start"
	TypeFileEnd   = "file-end"
	TypeFileAck   = "file-ack"
	TypeDone      = "done"
	TypeCancel    = "cancel"
	TypeError     = "error"
)

// FileAck status values (proto/TRANSFER.md rule 4).
const (
	StatusOK           = "ok"
	StatusHashMismatch = "hash-mismatch" // receiver keeps or deletes locally
)

// Error code values (proto/TRANSFER.md §Flow, §Versioning).
const (
	CodeProtocol = "protocol" // state violation, bad size, out-of-order message
	CodeVersion  = "version"  // unsupported Hello.V
)

// Hello is the first message each side sends; App is optional
// (e.g. "jsi-cli/0.1.0").
type Hello struct {
	Type string `json:"type"`
	V    int    `json:"v"`
	App  string `json:"app,omitempty"`
}

// FileMeta describes one file in a Manifest.
type FileMeta struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MIME string `json:"mime,omitempty"`
}

// Manifest lists the files the sender offers (rule 2: at most MaxFiles).
type Manifest struct {
	Type  string     `json:"type"`
	Files []FileMeta `json:"files"`
}

// Accept approves the transfer: nil or empty Files accepts the whole
// manifest, otherwise Files lists the accepted IDs (files are sent per
// accepted file, ascending ID).
type Accept struct {
	Type  string `json:"type"`
	Files []int  `json:"files,omitempty"`
}

// Reject declines the whole transfer.
type Reject struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// FileStart announces that the binary chunks of file ID follow.
type FileStart struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
}

// FileEnd ends the binary stream of file ID. SHA256 is the lowercase hex
// digest of exactly Size bytes, hashed while reading (rule 4, D10).
type FileEnd struct {
	Type   string `json:"type"`
	ID     int    `json:"id"`
	SHA256 string `json:"sha256"`
}

// FileAck reports the receiver's verdict for file ID: StatusOK or
// StatusHashMismatch, with Bytes actually received.
type FileAck struct {
	Type   string `json:"type"`
	ID     int    `json:"id"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
}

// Done ends the session after the last FileAck.
type Done struct {
	Type string `json:"type"`
}

// Cancel aborts the transfer at any time; the peer acknowledges by closing
// the channel.
type Cancel struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

// Error reports a protocol violation; the sender then closes the channel.
type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"` // CodeProtocol or CodeVersion
	Message string `json:"message"`
}

// Unknown is returned by Unmarshal for an unrecognized "type" (TP/1 rule 5:
// unknown types SHOULD be ignored after hello, for forward compatibility).
// It is receive-only; Marshal rejects it.
type Unknown struct {
	Type string `json:"type"`
}

var (
	// ErrNoType is returned by Unmarshal when "type" is missing or empty.
	ErrNoType = errors.New("protocol: missing message type")
	// ErrUnknownMessage is returned by Marshal for a value that is not one
	// of the TP/1 message structs above (including Unknown and nil).
	ErrUnknownMessage = errors.New("protocol: cannot marshal non-TP/1 message")
	// ErrNameNUL is returned by SanitizeName for a name containing NUL.
	ErrNameNUL = errors.New("protocol: name contains NUL byte")
	// ErrNameInvalid is returned by SanitizeName when no usable base name
	// remains ("", ".", "..").
	ErrNameInvalid = errors.New("protocol: name has no usable base after sanitization")
	// ErrNameTooLong is returned by SanitizeName for a name over
	// MaxNameBytes UTF-8 bytes.
	ErrNameTooLong = errors.New("protocol: name exceeds 255 bytes")
	// ErrSizeRange is returned by Validate for a size outside
	// 0 … MaxFileSize.
	ErrSizeRange = errors.New("protocol: size outside 0..2^53-1")
)

// Marshal encodes one TP/1 control message as a JSON text message. The
// "type" field is set from the concrete type of v, overwriting whatever is
// already there. v must be a message value (Hello, Manifest, … — not a
// pointer); anything else is rejected with ErrUnknownMessage.
func Marshal(v any) ([]byte, error) {
	switch m := v.(type) {
	case Hello:
		m.Type = TypeHello
		return json.Marshal(m)
	case Manifest:
		m.Type = TypeManifest
		return json.Marshal(m)
	case Accept:
		m.Type = TypeAccept
		return json.Marshal(m)
	case Reject:
		m.Type = TypeReject
		return json.Marshal(m)
	case FileStart:
		m.Type = TypeFileStart
		return json.Marshal(m)
	case FileEnd:
		m.Type = TypeFileEnd
		return json.Marshal(m)
	case FileAck:
		m.Type = TypeFileAck
		return json.Marshal(m)
	case Done:
		m.Type = TypeDone
		return json.Marshal(m)
	case Cancel:
		m.Type = TypeCancel
		return json.Marshal(m)
	case Error:
		m.Type = TypeError
		return json.Marshal(m)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnknownMessage, v)
	}
}

// Unmarshal decodes one JSON text message, dispatching on "type". An
// unrecognized type is NOT an error: it returns an Unknown carrying the
// raw type string, so the state machine can ignore it (rule 5). A missing
// or empty "type" is ErrNoType; malformed JSON — or a well-typed "type"
// whose remaining fields have the wrong JSON kind — is an error wrapping
// the encoding/json failure.
func Unmarshal(data []byte) (any, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("protocol: decode message: %w", err)
	}
	switch head.Type {
	case "":
		return nil, ErrNoType
	case TypeHello:
		return decode[Hello](data)
	case TypeManifest:
		return decode[Manifest](data)
	case TypeAccept:
		return decode[Accept](data)
	case TypeReject:
		return decode[Reject](data)
	case TypeFileStart:
		return decode[FileStart](data)
	case TypeFileEnd:
		return decode[FileEnd](data)
	case TypeFileAck:
		return decode[FileAck](data)
	case TypeDone:
		return decode[Done](data)
	case TypeCancel:
		return decode[Cancel](data)
	case TypeError:
		return decode[Error](data)
	default:
		return Unknown{Type: head.Type}, nil
	}
}

// decode unmarshals data into a fresh T and returns it as any.
func decode[T any](data []byte) (any, error) {
	var m T
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("protocol: decode %T: %w", m, err)
	}
	return m, nil
}

// SanitizeName maps a received manifest name to a safe local base name
// (TP/1 rule 1). Steps, in order:
//
//  1. A name containing NUL anywhere is rejected outright (ErrNameNUL).
//  2. The base is taken after the last '/' or '\'; both separators are
//     treated alike on every platform, so any directory prefix — including
//     ".." segments — is discarded.
//  3. A run of more than one leading dot is collapsed to a single dot:
//     "..foo" and "...foo" become the dotfile ".foo" rather than a parent
//     reference, while a normal dotfile (".bashrc") passes through
//     unchanged.
//  4. The result must be a real name: "", ".", or ".." (from a bare "..",
//     all-separator input, or a trailing separator) is rejected
//     (ErrNameInvalid).
//  5. A result longer than MaxNameBytes (255) in UTF-8 is rejected
//     (ErrNameTooLong); senders must not emit such names (rule 1).
//
// Trailing dots and other platform-specific quirks are left unchanged —
// SanitizeName guarantees only traversal-safety and the byte limit.
// Collisions with existing files are NOT handled here; the receiver in
// internal/transfer appends "-1", "-2", … per rule 1.
func SanitizeName(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", ErrNameNUL
	}
	base := name[strings.LastIndexAny(name, `/\`)+1:]
	dots := 0
	for dots < len(base) && base[dots] == '.' {
		dots++
	}
	if dots > 1 {
		base = "." + base[dots:]
	}
	if base == "" || base == "." || base == ".." {
		return "", ErrNameInvalid
	}
	if len(base) > MaxNameBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(base))
	}
	return base, nil
}

// Validate checks m's declared size against the TP/1 rule 2 range
// (0 … 2^53−1, the largest integer surviving a JSON number round-trip).
// Name is deliberately not checked: it is a sender obligation (rule 1) and
// receivers run SanitizeName instead.
func Validate(m FileMeta) error {
	if m.Size < 0 || m.Size > MaxFileSize {
		return fmt.Errorf("%w: id %d declares %d", ErrSizeRange, m.ID, m.Size)
	}
	return nil
}
