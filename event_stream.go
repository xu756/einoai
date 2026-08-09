package einoai

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type redisEventStream struct {
	store     *redisStore
	sessionID string
	runID     string
	lastID    string

	closeMu     sync.Mutex
	closed      bool
	closeCtx    context.Context
	closeCancel context.CancelFunc
}

func newRedisEventStream(store *redisStore, sessionID, runID, afterEventID string) *redisEventStream {
	if afterEventID == "" {
		afterEventID = "0-0"
	}
	closeCtx, closeCancel := context.WithCancel(context.Background())
	return &redisEventStream{
		store:       store,
		sessionID:   sessionID,
		runID:       runID,
		lastID:      afterEventID,
		closeCtx:    closeCtx,
		closeCancel: closeCancel,
	}
}

func (s *redisEventStream) Next(ctx context.Context) (*RunEvent, error) {
	readCtx, cancel, ok := s.readContext(ctx)
	if !ok {
		return nil, io.EOF
	}
	defer cancel()

	for {
		if err := readCtx.Err(); err != nil {
			if s.isClosed() {
				return nil, io.EOF
			}
			return nil, err
		}

		events, err := s.store.readAfter(readCtx, s.sessionID, s.runID, s.lastID, 15*time.Second, 1)
		if err != nil {
			if s.isClosed() && errors.Is(err, context.Canceled) {
				return nil, io.EOF
			}
			return nil, err
		}
		if len(events) > 0 {
			s.lastID = events[0].ID
			return events[0], nil
		}

		run, err := s.store.getRun(readCtx, s.sessionID, s.runID)
		if err != nil {
			if s.isClosed() && errors.Is(err, context.Canceled) {
				return nil, io.EOF
			}
			return nil, err
		}
		if run == nil || isTerminalRunStatus(run.Status) {
			return nil, io.EOF
		}
	}
}

func (s *redisEventStream) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.ensureCloseContextLocked()
	s.closed = true
	s.closeCancel()
	return nil
}

func (s *redisEventStream) readContext(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil, nil, false
	}
	s.ensureCloseContextLocked()
	closeCtx := s.closeCtx
	s.closeMu.Unlock()

	readCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(closeCtx, cancel)
	return readCtx, func() {
		stop()
		cancel()
	}, true
}

func (s *redisEventStream) ensureCloseContextLocked() {
	if s.closeCtx != nil {
		return
	}
	s.closeCtx, s.closeCancel = context.WithCancel(context.Background())
}

func (s *redisEventStream) isClosed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closed
}
