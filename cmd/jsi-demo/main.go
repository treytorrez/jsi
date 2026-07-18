// jsi-demo is a HACKY demo binary thrown together on spike/demo — it is NOT
// the real CLI (that lands in M3, see PLAN.md §7.5). It wires the production
// internal/peer + internal/signal.Paste + internal/transfer stack together
// with hardcoded choices: paste signaling (QP/1), host candidates only,
// zero infrastructure contact. Blobs go to stdout; humans get stderr.
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/treyt/jsi/internal/peer"
	sig "github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: jsi-demo send <file> | jsi-demo receive <dir>")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var err error
	switch os.Args[1] {
	case "send":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: jsi-demo send <file>")
		} else {
			err = send(ctx, os.Args[2])
		}
	case "receive":
		dir := "."
		if len(os.Args) == 3 {
			dir = os.Args[2]
		}
		err = receive(ctx, dir)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jsi-demo:", err)
		os.Exit(1)
	}
}

// drainEvents prints sparse progress to stderr.
func drainEvents(ev chan transfer.Event) {
	var last int64
	for e := range ev {
		switch e.Kind {
		case transfer.EventProgress:
			if e.BytesDone-last >= 1<<20 || e.BytesDone == e.BytesTotal {
				fmt.Fprintf(os.Stderr, "  … %d / %d bytes\n", e.BytesDone, e.BytesTotal)
				last = e.BytesDone
			}
		case transfer.EventFileDone:
			fmt.Fprintf(os.Stderr, "  file %d done (%d bytes)\n", e.FileID, e.BytesTotal)
		case transfer.EventError:
			fmt.Fprintln(os.Stderr, "  error:", e.Err)
		}
	}
}

func send(ctx context.Context, path string) error {
	conn, offer, err := peer.Offer(ctx, peer.Config{EnableMDNS: false}) // host candidates only
	if err != nil {
		return err
	}
	defer conn.Close()

	p := &sig.Paste{In: os.Stdin, Out: os.Stdout}
	if _, wait, err := p.Announce(ctx, offer); err != nil { // writes offer blob to stdout
		return err
	} else {
		fmt.Fprintln(os.Stderr, ">>> offer blob printed. Paste it into the receiver, then paste the answer blob here.")
		answer, err := wait(ctx)
		if err != nil {
			return err
		}
		if err := conn.SetRemote(ctx, answer); err != nil {
			return err
		}
	}

	if err := conn.WaitOpen(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, ">>> P2P data channel open (DTLS-encrypted). Sending…")

	files, err := transfer.FilesFromPaths([]string{path})
	if err != nil {
		return err
	}
	ev := make(chan transfer.Event, 64)
	go drainEvents(ev)
	start := time.Now()
	if err := transfer.Send(ctx, conn, files, ev); err != nil {
		return err
	}
	sum := sha256.Sum256(mustRead(path))
	fmt.Fprintf(os.Stderr, "SENT %s (%d bytes) in %s\nsha256: %x\n", filepath.Base(path), files[0].Size, time.Since(start).Round(time.Millisecond), sum)
	return nil
}

func receive(ctx context.Context, dir string) error {
	p := &sig.Paste{In: os.Stdin, Out: os.Stdout}
	offer, respond, err := p.Join(ctx, "") // reads offer blob from stdin
	if err != nil {
		return err
	}
	conn, answer, err := peer.Answer(ctx, peer.Config{EnableMDNS: false}, offer)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := respond(ctx, answer); err != nil { // writes answer blob to stdout
		return err
	}
	fmt.Fprintln(os.Stderr, ">>> answer blob printed. Paste it back into the sender.")

	if err := conn.WaitOpen(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, ">>> P2P data channel open (DTLS-encrypted). Receiving into", dir)

	ev := make(chan transfer.Event, 64)
	go drainEvents(ev)
	if err := transfer.Receive(ctx, conn, dir, ev); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "RECEIVED OK ->", dir)
	return nil
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return b
}
