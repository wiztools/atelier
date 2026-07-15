package main

import (
	"bufio"
	"io"
	"strings"
)

// streamLines reads a newline-delimited response body and sends one ChatEvent
// per line via decode. It owns the shared SSE scaffolding every provider's
// StreamChat needs: a buffered scanner, per-line trimming, the done/error event
// convention, and the terminal scanner-err check. Each provider supplies only
// its per-line decode rule (which may signal "terminate stream" or "error").
//
// decode returns:
//   - the ChatEvent to emit (sent as-is),
//   - terminate=true to stop the stream after emitting (e.g. a done marker),
//   - a non-nil error to emit a terminal error event and stop.
//
// streamLines closes events when the loop ends, but the caller owns resp.Body
// and must close it (typically via defer in the goroutine that calls this).
func streamLines(body io.Reader, events chan<- ChatEvent, decode func(line string) (event ChatEvent, terminate bool, err error)) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, terminate, err := decode(line)
		if err != nil {
			events <- ChatEvent{Err: err, Done: true}
			return
		}
		events <- event
		if terminate {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		events <- ChatEvent{Err: err, Done: true}
	}
}
