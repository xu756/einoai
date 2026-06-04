package enioai

import (
	"encoding/json"
	"strings"
)

type contentPart struct {
	reasoning bool
	text      string
}

type thinkSplitter struct {
	inThink bool
	pending string
}

func (s *thinkSplitter) feed(chunk string) []contentPart {
	s.pending += chunk
	return s.drain(false)
}

func (s *thinkSplitter) flush() []contentPart {
	return s.drain(true)
}

func (s *thinkSplitter) drain(flush bool) []contentPart {
	var out []contentPart
	for {
		tag := "<think>"
		if s.inThink {
			tag = "</think>"
		}
		idx := strings.Index(s.pending, tag)
		if idx >= 0 {
			if idx > 0 {
				out = append(out, contentPart{reasoning: s.inThink, text: s.pending[:idx]})
			}
			s.pending = s.pending[idx+len(tag):]
			s.inThink = !s.inThink
			continue
		}
		if flush {
			if s.pending != "" {
				out = append(out, contentPart{reasoning: s.inThink, text: s.pending})
				s.pending = ""
			}
			return out
		}
		keep := longestTagPrefixSuffix(s.pending, tag)
		flushLen := len(s.pending) - keep
		if flushLen > 0 {
			out = append(out, contentPart{reasoning: s.inThink, text: s.pending[:flushLen]})
			s.pending = s.pending[flushLen:]
		}
		return out
	}
}

func longestTagPrefixSuffix(s string, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}

func parseMaybeJSON(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}
