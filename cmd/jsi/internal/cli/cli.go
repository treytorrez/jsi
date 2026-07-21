// Package cli implements the jsi command-line interface (PLAN.md §7.5):
// send/receive flows over the internal peer/signal/transfer stack, the D15
// externals policy, and the M3.4 exit codes. stdout carries QP/1 paste
// blobs only — every prompt, notice, QR, and progress bar goes to stderr —
// so paste mode stays pipe-safe (proto/SIGNALING.md §Paste format).
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/treyt/jsi/internal/policy"
)

// DefaultServerURL is the hosted SP/1 worker (deployed 2026-07-18,
// URL is set at M1.8 deploy (PLAN.md §7.1, proto/SIGNALING.md §Base URL).
const DefaultServerURL = "https://jsi-signal.treytorrez.workers.dev"

// Main is the program entry: SIGINT cancels the run's context (an in-flight
// transfer then sends a TP/1 cancel, M3.4) and the result maps to an exit
// code.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	verbose, err := run(ctx, args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		printError(os.Stderr, err, verbose)
	}
	return ExitCode(err, ctx.Err() != nil)
}

// app carries one command's streams and resolved options.
type app struct {
	stdin  *bufio.Reader // shared with the paste channel (see readLine)
	stdout io.Writer
	stderr io.Writer

	verbose bool
	yes     bool
	server  string
	pwaURL  string
	policy  policy.Policy
	noSTUN  bool
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 {
		usage(stderr)
		return false, errors.New("no command (want send or receive)")
	}
	a := &app{stdin: bufio.NewReader(stdin), stdout: stdout, stderr: stderr}
	switch args[0] {
	case "send":
		return a.send(ctx, args[1:])
	case "receive":
		return a.receive(ctx, args[1:])
	case "-h", "--help", "help":
		usage(stdout)
		return false, nil
	default:
		usage(stderr)
		return false, fmt.Errorf("unknown command %q (want send or receive)", args[0])
	}
}

// printError prints the M3.4 one-line error; -v adds the full chain.
func printError(w io.Writer, err error, verbose bool) {
	friendly := friendlyMessage(err)
	if friendly != "" {
		eprintf(w, "jsi: %s\n", friendly)
		if !verbose {
			return
		}
	}
	eprintf(w, "jsi: %v\n", err)
}

// CLI output is best-effort: a write failure to stderr (closed pipe) is not
// actionable, so the e-helpers deliberately drop print errors. (errcheck's
// default exclusions cover os.Stderr directly, not io.Writer fields.)
func eprint(w io.Writer, args ...any)                 { _, _ = fmt.Fprint(w, args...) }
func eprintf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
func eprintln(w io.Writer, args ...any)               { _, _ = fmt.Fprintln(w, args...) }

// flagValues holds the flags common to send and receive (PLAN.md §7.5).
type flagValues struct {
	externals string
	signal    string
	relay     bool
	noRelay   bool
	server    string
	pwaURL    string
	noMDNS    bool
	noSTUN    bool
	yes       bool
	verbose   bool
}

func newFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *flagValues) {
	fv := &flagValues{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&fv.externals, "externals", policy.ExternalsNone, "external-services policy: none|fallback|full")
	fs.StringVar(&fv.signal, "signal", "", "signaling channel: paste|qr|worker (default: from --externals preset)")
	fs.BoolVar(&fv.relay, "relay", false, "allow TURN relay (default: from preset)")
	fs.BoolVar(&fv.noRelay, "no-relay", false, "forbid TURN relay (default: from preset)")
	fs.StringVar(&fv.server, "server", DefaultServerURL, "signaling worker URL")
	fs.StringVar(&fv.pwaURL, "pwa-url", "", "base URL for the send-side QR payload (default: bare token)")
	fs.BoolVar(&fv.noMDNS, "no-mdns", false, "disable mDNS host candidates (escape hatch for networks where multicast is broken)")
	fs.BoolVar(&fv.noSTUN, "no-stun", false, "host candidates only — no STUN requests (zero external contact; best for offline LANs)")
	fs.BoolVar(&fv.yes, "yes", false, "accept the transfer without prompting")
	fs.BoolVar(&fv.yes, "y", false, "shorthand for --yes")
	fs.BoolVar(&fv.verbose, "v", false, "verbose errors")
	return fs, fv
}

// resolve maps the raw flags to a Policy, catching the one contradictory
// combination.
func (fv *flagValues) resolve() (policy.Policy, error) {
	relay := policy.RelayUnset
	switch {
	case fv.relay && fv.noRelay:
		return policy.Policy{}, errors.New("--relay and --no-relay are mutually exclusive")
	case fv.relay:
		relay = policy.RelayOn
	case fv.noRelay:
		relay = policy.RelayOff
	}
	return policy.ResolvePolicy(fv.externals, fv.signal, relay)
}

// errHelp is returned by parseFlags when -h/--help is seen.
var errHelp = errors.New("help requested")

// parseFlags is stdlib flag parsing with interspersed positionals: the
// stdlib stops at the first non-flag argument, but jsi flags work anywhere
// ("jsi send file --externals full"). It returns the positional arguments.
// "--" ends flag parsing; a bare "-" is a positional.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		value, hasValue := "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, value, hasValue = name[:j], name[j+1:], true
		}
		if name == "h" || name == "help" {
			return nil, errHelp
		}
		fl := fs.Lookup(name)
		if fl == nil {
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
		_, isBool := fl.Value.(interface{ IsBoolFlag() bool })
		switch {
		case isBool && !hasValue:
			value = "true"
		case !hasValue:
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("flag --%s needs a value", name)
			}
			value = args[i]
		}
		if err := fl.Value.Set(value); err != nil {
			return nil, fmt.Errorf("flag --%s: %w", name, err)
		}
	}
	return positional, nil
}

// readLine reads one trimmed line from stdin. The paste channel reads its
// blobs from the same *bufio.Reader, so buffered input is never stranded
// between the two readers.
func (a *app) readLine() (string, error) {
	line, err := a.stdin.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// humanBytes renders n in IEC units for file lists and summaries.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// usage prints the command summary. In paste mode stdout is blob-only, so
// the caller picks the stream: stderr on error, stdout on explicit help.
func usage(w io.Writer) {
	eprintf(w, `jsi — Just Send It: P2P file transfer over WebRTC data channels

usage:
  jsi send <file...> [flags]
  jsi receive [token] [flags]

flags (both commands):
  --externals none|fallback|full  external-services policy (default "none":
                                  paste signaling + STUN, zero server contact)
  --signal paste|worker           signaling channel (default: from preset)
  --relay, --no-relay             allow/forbid TURN relay (default: from preset)
  --server URL                    signaling worker (default %s)
  --pwa-url URL                   base URL for the send-side QR (default: bare token)
  -y, --yes                       accept the transfer without prompting
  -v                              verbose errors
receive only:
  -o dir                          destination directory (default ".")

exit codes: 0 ok · 1 error · 2 signaling timeout · 3 peer rejected · 4 integrity failure · 130 interrupted

paste mode: "jsi1:" offer/answer blobs are the only stdout output (pipe-safe);
all prompts and progress go to stderr.
`, DefaultServerURL)
}
