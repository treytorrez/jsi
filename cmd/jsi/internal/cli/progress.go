package cli

import (
	"io"
	"time"

	"github.com/schollz/progressbar/v3"

	"github.com/treyt/jsi/internal/transfer"
)

// renderEvents drains a transfer event channel, driving one
// progressbar/v3 bar per file (M3.3). Bars render on stderr so stdout
// stays blob-only. names maps TP/1 file ID → display name (sender: the
// send list; receiver: the manifest, filled by makeDecide).
func (a *app) renderEvents(ev <-chan transfer.Event, names map[int]string) {
	var bar *progressbar.ProgressBar
	for e := range ev {
		switch e.Kind {
		case transfer.EventFileStart:
			if e.BytesTotal > 0 {
				bar = newBar(a.stderr, e.BytesTotal, names[e.FileID])
			}
		case transfer.EventProgress:
			if bar != nil {
				_ = bar.Set64(e.BytesDone)
			}
		case transfer.EventFileDone:
			if bar != nil {
				_ = bar.Finish()
				bar = nil
			}
			if e.Err == nil {
				eprintf(a.stderr, "  ok %s (%s)\n", names[e.FileID], humanBytes(e.BytesTotal))
			}
		case transfer.EventDone, transfer.EventError:
			// Terminal: the caller prints the outcome.
		}
	}
}

// newBar builds one bytes bar (progressbar/v3) with the DefaultBytes
// option shape, writing to w.
func newBar(w io.Writer, total int64, desc string) *progressbar.ProgressBar {
	return progressbar.NewOptions64(total,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetWriter(w),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionSetWidth(10),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() { eprint(w, "\n") }),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetRenderBlankState(true),
	)
}
