package protocol

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// sha256Empty is the SHA-256 of zero bytes, used as realistic hex test data.
const sha256Empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestConstants(t *testing.T) {
	if Version != 1 {
		t.Errorf("Version = %d, want 1 (TP/1)", Version)
	}
	if ChunkSize != 16384 {
		t.Errorf("ChunkSize = %d, want 16384", ChunkSize)
	}
	if LowWatermarkBytes >= HighWatermarkBytes || HighWatermarkBytes >= HardBufferedLimitBytes {
		t.Errorf("watermarks out of order: low %d, high %d, hard %d",
			LowWatermarkBytes, HighWatermarkBytes, HardBufferedLimitBytes)
	}
}

func TestMarshalExactJSON(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want string // exact wire bytes; encoding/json emits fields in struct order
	}{
		{"hello with app", Hello{V: Version, App: "jsi-cli/0.1.0"},
			`{"type":"hello","v":1,"app":"jsi-cli/0.1.0"}`},
		{"hello without app", Hello{V: Version},
			`{"type":"hello","v":1}`},
		{"manifest", Manifest{Files: []FileMeta{
			{ID: 0, Name: "a.bin", Size: 123456, MIME: "application/octet-stream"},
			{ID: 1, Name: "b", Size: 0},
		}},
			`{"type":"manifest","files":[{"id":0,"name":"a.bin","size":123456,"mime":"application/octet-stream"},{"id":1,"name":"b","size":0}]}`},
		{"accept all (nil files)", Accept{},
			`{"type":"accept"}`},
		{"accept all (empty files)", Accept{Files: []int{}},
			`{"type":"accept"}`},
		{"accept subset", Accept{Files: []int{0, 2}},
			`{"type":"accept","files":[0,2]}`},
		{"reject", Reject{Reason: "no thanks"},
			`{"type":"reject","reason":"no thanks"}`},
		{"file-start", FileStart{ID: 0},
			`{"type":"file-start","id":0}`},
		{"file-end", FileEnd{ID: 0, SHA256: sha256Empty},
			`{"type":"file-end","id":0,"sha256":"` + sha256Empty + `"}`},
		{"file-ack ok", FileAck{ID: 0, Status: StatusOK, Bytes: 123456},
			`{"type":"file-ack","id":0,"status":"ok","bytes":123456}`},
		{"file-ack hash-mismatch", FileAck{ID: 0, Status: StatusHashMismatch, Bytes: 123456},
			`{"type":"file-ack","id":0,"status":"hash-mismatch","bytes":123456}`},
		{"done", Done{},
			`{"type":"done"}`},
		{"cancel with reason", Cancel{Reason: "user abort"},
			`{"type":"cancel","reason":"user abort"}`},
		{"cancel without reason", Cancel{},
			`{"type":"cancel"}`},
		{"error version", Error{Code: CodeVersion, Message: "unsupported TP version"},
			`{"type":"error","code":"version","message":"unsupported TP version"}`},
		{"error protocol", Error{Code: CodeProtocol, Message: "file-end before size reached"},
			`{"type":"error","code":"protocol","message":"file-end before size reached"}`},
		// Marshal owns "type": a stale or wrong preset value is overwritten.
		{"type field overwritten", Done{Type: "bogus"},
			`{"type":"done"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Marshal(tt.msg)
			if err != nil {
				t.Fatalf("Marshal(%T) error: %v", tt.msg, err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal(%T) = %s, want %s", tt.msg, got, tt.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		msg  any // Type pre-set, as Marshal leaves it
	}{
		{"hello with app", Hello{Type: TypeHello, V: Version, App: "jsi-cli/0.1.0"}},
		{"hello without app", Hello{Type: TypeHello, V: Version}},
		{"manifest empty", Manifest{Type: TypeManifest, Files: []FileMeta{}}},
		{"manifest with files", Manifest{Type: TypeManifest, Files: []FileMeta{
			{ID: 0, Name: "a.bin", Size: 123456, MIME: "application/octet-stream"},
			{ID: 2, Name: "ünïcode 🚀.bin", Size: MaxFileSize},
		}}},
		{"accept all", Accept{Type: TypeAccept}},
		{"accept subset", Accept{Type: TypeAccept, Files: []int{0, 2}}},
		{"reject", Reject{Type: TypeReject, Reason: "storage full"}},
		{"file-start", FileStart{Type: TypeFileStart, ID: 7}},
		{"file-end", FileEnd{Type: TypeFileEnd, ID: 7, SHA256: sha256Empty}},
		{"file-ack ok", FileAck{Type: TypeFileAck, ID: 7, Status: StatusOK, Bytes: 123456}},
		{"file-ack hash-mismatch", FileAck{Type: TypeFileAck, ID: 7, Status: StatusHashMismatch, Bytes: 123456}},
		{"done", Done{Type: TypeDone}},
		{"cancel with reason", Cancel{Type: TypeCancel, Reason: "user abort"}},
		{"cancel without reason", Cancel{Type: TypeCancel}},
		{"error protocol", Error{Type: TypeError, Code: CodeProtocol, Message: "unexpected chunk"}},
		{"error version", Error{Type: TypeError, Code: CodeVersion, Message: "unsupported TP version"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Marshal(tt.msg)
			if err != nil {
				t.Fatalf("Marshal(%T) error: %v", tt.msg, err)
			}
			got, err := Unmarshal(data)
			if err != nil {
				t.Fatalf("Unmarshal(Marshal(%T)) error: %v", tt.msg, err)
			}
			if !reflect.DeepEqual(got, tt.msg) {
				t.Errorf("round trip = %#v, want %#v (wire: %s)", got, tt.msg, data)
			}
		})
	}
}

func TestMarshalRejectsNonMessage(t *testing.T) {
	for _, v := range []any{
		nil,
		"hello",
		42,
		struct{}{},
		Unknown{Type: "future-thing"}, // receive-only per rule 5
		&Done{},                       // values only, no pointers
	} {
		if _, err := Marshal(v); !errors.Is(err, ErrUnknownMessage) {
			t.Errorf("Marshal(%T) error = %v, want ErrUnknownMessage", v, err)
		}
	}
}

func TestUnmarshalUnknownType(t *testing.T) {
	got, err := Unmarshal([]byte(`{"type":"future-thing","extra":42}`))
	if err != nil {
		t.Fatalf("Unmarshal(unknown type) error: %v (rule 5: ignore, not error)", err)
	}
	if want := (Unknown{Type: "future-thing"}); got != want {
		t.Errorf("Unmarshal(unknown type) = %#v, want %#v", got, want)
	}
}

func TestUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr error // sentinel matched with errors.Is; nil = any non-nil error
	}{
		{"empty input", ``, nil},
		{"truncated object", `{`, nil},
		{"not json at all", `not json`, nil},
		{"json array", `[]`, nil},
		{"json string", `"hello"`, nil},
		{"json number", `123`, nil},
		{"json null", `null`, ErrNoType},
		{"empty object", `{}`, ErrNoType},
		{"empty type", `{"type":""}`, ErrNoType},
		{"non-string type", `{"type":123}`, nil},
		{"field of wrong kind", `{"type":"hello","v":"one"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Unmarshal([]byte(tt.in))
			if err == nil {
				t.Fatalf("Unmarshal(%s) = %#v, want error", tt.in, got)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("Unmarshal(%s) error = %v, want errors.Is %v", tt.in, err, tt.wantErr)
			}
			if got != nil {
				t.Errorf("Unmarshal(%s) returned %#v alongside error, want nil", tt.in, got)
			}
		})
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error // sentinel matched with errors.Is; nil = success
	}{
		// Clean names pass through.
		{"clean", "report.pdf", "report.pdf", nil},
		{"normal dotfile", ".bashrc", ".bashrc", nil},
		{"unicode", "ünïcode 🚀.bin", "ünïcode 🚀.bin", nil},
		// Directory prefixes collapse to the base after any '/' or '\'.
		{"subdirectory", "docs/report.pdf", "report.pdf", nil},
		{"unix traversal", "../../etc/passwd", "passwd", nil},
		{"windows path", `C:\Users\a\file.txt`, "file.txt", nil},
		{"windows traversal", `..\..\win\system32\cfg.exe`, "cfg.exe", nil},
		{"mixed separators", `a/b\c.txt`, "c.txt", nil},
		// Leading-dot runs longer than one collapse to a single dot.
		{"dotdot collapsed", "..ssh", ".ssh", nil},
		{"many dots collapsed", "...rc", ".rc", nil},
		// No usable base remains.
		{"bare dotdot", "..", "", ErrNameInvalid},
		{"single dot", ".", "", ErrNameInvalid},
		{"only dots", "...", "", ErrNameInvalid},
		{"trailing separator", "a/b/", "", ErrNameInvalid},
		{"only separators", `\/`, "", ErrNameInvalid},
		{"empty", "", "", ErrNameInvalid},
		// Pinned behavior: trailing dots are preserved (documented).
		{"trailing dot kept", "notes.", "notes.", nil},
		// NUL is rejected outright, even in a discarded directory part.
		{"NUL", "a\x00b", "", ErrNameNUL},
		{"NUL in directory part", "a\x00/b", "", ErrNameNUL},
		// Byte limit counts UTF-8 bytes, not runes.
		{"exactly 255 bytes", strings.Repeat("a", MaxNameBytes), strings.Repeat("a", MaxNameBytes), nil},
		{"256 bytes", strings.Repeat("a", MaxNameBytes+1), "", ErrNameTooLong},
		{"multibyte at limit", strings.Repeat("é", 127) + "a", strings.Repeat("é", 127) + "a", nil},
		{"multibyte over limit", strings.Repeat("é", 128), "", ErrNameTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeName(tt.in)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("SanitizeName(%q) error: %v", tt.in, err)
				}
				if got != tt.want {
					t.Errorf("SanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("SanitizeName(%q) error = %v, want errors.Is %v", tt.in, err, tt.wantErr)
			}
			if got != "" {
				t.Errorf("SanitizeName(%q) returned %q alongside error, want \"\"", tt.in, got)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		size    int64
		wantErr error
	}{
		{"zero", 0, nil},
		{"one byte", 1, nil},
		{"max json-safe", MaxFileSize, nil},
		{"over max", MaxFileSize + 1, ErrSizeRange},
		{"negative", -1, ErrSizeRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(FileMeta{ID: 0, Name: "a", Size: tt.size})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate(size %d) = %v, want errors.Is %v", tt.size, err, tt.wantErr)
			}
		})
	}
}
