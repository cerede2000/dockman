package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

const (
	// defaultStreamTail bounds the history replayed per container when the
	// client does not ask for a specific amount
	defaultStreamTail = 1000

	StreamStdout int32 = 1
	StreamStderr int32 = 2
)

// LogsStreamOptions mirrors the ContainerLogsStream request
type LogsStreamOptions struct {
	Tail   int32
	Since  int64 // unix seconds, 0 = unbounded
	Until  int64
	Follow bool
}

// LogLine is one demuxed container log line; TimeNano is 0 when the daemon
// line carried no parsable timestamp
type LogLine struct {
	ContainerID   string
	ContainerName string
	Text          string
	TimeNano      int64
	Stream        int32
}

// LogsStream reads the logs of every requested container concurrently and
// calls emit for each line until all readers finish (follow=false) or ctx is
// canceled. emit may be called from multiple goroutines.
func (s *Service) LogsStream(ctx context.Context, containerIDs []string, opts LogsStreamOptions, emit func(LogLine)) error {
	if len(containerIDs) == 0 {
		return fmt.Errorf("at least one container id is required")
	}

	tail := opts.Tail
	if tail <= 0 {
		tail = defaultStreamTail
	}

	logOpts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		// always ask the daemon for timestamps: they are stripped from the
		// text and carried separately so the client can toggle them
		Timestamps: true,
		Follow:     opts.Follow,
		Tail:       strconv.Itoa(int(tail)),
	}
	if opts.Since > 0 {
		logOpts.Since = strconv.FormatInt(opts.Since, 10)
	}
	if opts.Until > 0 {
		logOpts.Until = strconv.FormatInt(opts.Until, 10)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, id := range containerIDs {
		wg.Add(1)
		go func(containerID string) {
			defer wg.Done()
			err := s.streamContainerLogs(streamCtx, containerID, logOpts, emit)
			if err != nil && streamCtx.Err() == nil {
				log.Warn().Err(err).Str("container", containerID).Msg("container log stream failed")
				// surface the failure in the viewer instead of dying silently
				emit(LogLine{
					ContainerID:   containerID,
					ContainerName: containerID,
					Text:          "dockman: log stream error: " + err.Error(),
					TimeNano:      time.Now().UnixNano(),
					Stream:        StreamStderr,
				})
			}
		}(id)
	}
	wg.Wait()
	return nil
}

func (s *Service) streamContainerLogs(ctx context.Context, containerID string, logOpts client.ContainerLogsOptions, emit func(LogLine)) error {
	inspect, err := s.Client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("unable to inspect container: %w", err)
	}
	name := strings.TrimPrefix(inspect.Container.Name, "/")

	reader, err := s.Client.ContainerLogs(ctx, containerID, logOpts)
	if err != nil {
		return fmt.Errorf("unable to open container logs: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// the demux loop below blocks on reader.Reader; closing the reader when the
	// client goes away is what unblocks it
	go func() {
		<-ctx.Done()
		_ = reader.Close()
	}()

	stdout := &logLineWriter{id: containerID, name: name, stream: StreamStdout, emit: emit}
	stderr := &logLineWriter{id: containerID, name: name, stream: StreamStderr, emit: emit}
	defer stdout.Flush()
	defer stderr.Flush()

	if inspect.Container.Config.Tty {
		// tty streams have no multiplex framing, everything is stdout
		_, err = io.Copy(stdout, reader)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, reader)
	}
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// logLineWriter splits a raw log byte stream into lines, keeping the trailing
// partial line between writes until its newline (or Flush) arrives
type logLineWriter struct {
	id      string
	name    string
	stream  int32
	emit    func(LogLine)
	partial []byte
}

func (w *logLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			w.partial = append(w.partial, p...)
			return n, nil
		}
		line := p[:idx]
		if len(w.partial) > 0 {
			line = append(w.partial, line...)
			w.partial = nil
		}
		w.emitLine(line)
		p = p[idx+1:]
	}
}

// Flush emits the pending partial line, if any; call it when the stream ends
func (w *logLineWriter) Flush() {
	if len(w.partial) > 0 {
		w.emitLine(w.partial)
		w.partial = nil
	}
}

func (w *logLineWriter) emitLine(line []byte) {
	text := strings.TrimSuffix(string(line), "\r")
	timeNano, text := splitLogTimestamp(text)
	w.emit(LogLine{
		ContainerID:   w.id,
		ContainerName: w.name,
		Text:          text,
		TimeNano:      timeNano,
		Stream:        w.stream,
	})
}

// splitLogTimestamp strips the RFC3339Nano prefix the daemon adds when logs
// are requested with Timestamps: true; lines without one pass through as-is
func splitLogTimestamp(line string) (int64, string) {
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return 0, line
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:idx])
	if err != nil {
		return 0, line
	}
	return ts.UnixNano(), line[idx+1:]
}
