package enioai

import "testing"

func TestThinkSplitterHandlesSplitTags(t *testing.T) {
	var splitter thinkSplitter
	var parts []contentPart

	for _, chunk := range []string{"hello <thi", "nk>why", "</thi", "nk> world"} {
		parts = append(parts, splitter.feed(chunk)...)
	}
	parts = append(parts, splitter.flush()...)

	want := []contentPart{
		{text: "hello "},
		{reasoning: true, text: "why"},
		{text: " world"},
	}
	if len(parts) != len(want) {
		t.Fatalf("expected %d parts, got %#v", len(want), parts)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("part %d: expected %#v, got %#v", i, want[i], parts[i])
		}
	}
}
