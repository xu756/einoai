package einoai

import (
	"context"
	"io"
	"time"
)

type redisEventStream struct {
	store     *redisStore
	sessionID string
	runID     string
	lastID    string
	closed    bool
}

func (s *redisEventStream) Next(ctx context.Context) (*RunEvent, error) {
	if s.closed {
		return nil, io.EOF
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		events, err := s.store.readAfter(ctx, s.sessionID, s.runID, s.lastID, 15*time.Second, 1)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			s.lastID = events[0].ID
			return events[0], nil
		}

		run, err := s.store.getRun(ctx, s.sessionID, s.runID)
		if err != nil {
			return nil, err
		}
		if run == nil || isTerminalRunStatus(run.Status) {
			return nil, io.EOF
		}
	}
}

func (s *redisEventStream) Close() error {
	s.closed = true
	return nil
}
