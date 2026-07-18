package cli

import (
	"errors"
	"flag"
	"io"
	"reflect"
	"testing"
)

func testFlagSet() (*flag.FlagSet, *flagValues) {
	return newFlagSet("test", io.Discard)
}

// TestParseFlags covers the interspersed-flags parser: stdlib flag stops at
// the first positional, but jsi flags work anywhere.
func TestParseFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T, fv *flagValues, pos []string, err error)
	}{
		{"flags after positional", []string{"file.txt", "--externals", "full"},
			func(t *testing.T, fv *flagValues, pos []string, err error) {
				if err != nil || fv.externals != "full" || !reflect.DeepEqual(pos, []string{"file.txt"}) {
					t.Errorf("got fv=%+v pos=%v err=%v", fv, pos, err)
				}
			}},
		{"equals form", []string{"--externals=fallback", "a", "b"},
			func(t *testing.T, fv *flagValues, pos []string, err error) {
				if err != nil || fv.externals != "fallback" || !reflect.DeepEqual(pos, []string{"a", "b"}) {
					t.Errorf("got fv=%+v pos=%v err=%v", fv, pos, err)
				}
			}},
		{"bool flag bare", []string{"--no-relay", "-y", "f"},
			func(t *testing.T, fv *flagValues, pos []string, err error) {
				if err != nil || !fv.noRelay || !fv.yes || !reflect.DeepEqual(pos, []string{"f"}) {
					t.Errorf("got fv=%+v pos=%v err=%v", fv, pos, err)
				}
			}},
		{"bool flag with explicit value", []string{"--relay=false"},
			func(t *testing.T, fv *flagValues, pos []string, err error) {
				if err != nil || fv.relay || len(pos) != 0 {
					t.Errorf("got fv=%+v pos=%v err=%v", fv, pos, err)
				}
			}},
		{"double dash ends flags", []string{"--", "--externals"},
			func(t *testing.T, fv *flagValues, pos []string, err error) {
				if err != nil || fv.externals != ExternalsNone || !reflect.DeepEqual(pos, []string{"--externals"}) {
					t.Errorf("got fv=%+v pos=%v err=%v", fv, pos, err)
				}
			}},
		{"bare dash is positional", []string{"-"},
			func(t *testing.T, fv *flagValues, pos []string, err error) {
				if err != nil || !reflect.DeepEqual(pos, []string{"-"}) {
					t.Errorf("got fv=%+v pos=%v err=%v", fv, pos, err)
				}
			}},
		{"unknown flag", []string{"--bogus"},
			func(t *testing.T, _ *flagValues, _ []string, err error) {
				if err == nil || errors.Is(err, errHelp) {
					t.Errorf("got err=%v, want unknown-flag error", err)
				}
			}},
		{"missing value", []string{"--externals"},
			func(t *testing.T, _ *flagValues, _ []string, err error) {
				if err == nil {
					t.Error("got nil error, want needs-a-value error")
				}
			}},
		{"help", []string{"--help"},
			func(t *testing.T, _ *flagValues, _ []string, err error) {
				if !errors.Is(err, errHelp) {
					t.Errorf("got err=%v, want errHelp", err)
				}
			}},
		{"short help", []string{"-h"},
			func(t *testing.T, _ *flagValues, _ []string, err error) {
				if !errors.Is(err, errHelp) {
					t.Errorf("got err=%v, want errHelp", err)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, fv := testFlagSet()
			pos, err := parseFlags(fs, tt.args)
			tt.check(t, fv, pos, err)
		})
	}
}

// TestResolveFlags covers the raw-flags → Policy step, including the one
// contradictory combination.
func TestResolveFlags(t *testing.T) {
	fv := &flagValues{externals: ExternalsNone, relay: true, noRelay: true}
	if _, err := fv.resolve(); err == nil {
		t.Error("--relay with --no-relay: got nil error, want conflict")
	}

	fv = &flagValues{externals: ExternalsFull}
	pol, err := fv.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if pol.Signal != SignalWorker || !pol.Relay || !pol.FetchICE {
		t.Errorf("resolve(full): got %+v", pol)
	}
}
