package services

import (
	"strings"
	"testing"
)

func TestTikTokChunkPlan(t *testing.T) {
	const mb = int64(1024 * 1024)
	cases := []struct {
		size, wantChunk, wantCount int64
	}{
		{3 * mb, 3 * mb, 1},   // under 5 MB: single chunk
		{64 * mb, 64 * mb, 1}, // up to 64 MB: single chunk
		{65 * mb, 10 * mb, 6}, // last chunk absorbs the 5 MB remainder
		{1000 * mb, 10 * mb, 100},
	}
	for _, c := range cases {
		chunk, count := tiktokChunkPlan(c.size)
		if chunk != c.wantChunk || count != c.wantCount {
			t.Errorf("size %d: got (%d, %d), want (%d, %d)", c.size, chunk, count, c.wantChunk, c.wantCount)
		}
		// The final chunk (size - (count-1)*chunk) must stay within TikTok's 128 MB cap.
		if last := c.size - (count-1)*chunk; last > 128*mb {
			t.Errorf("size %d: last chunk %d exceeds 128 MB", c.size, last)
		}
	}
}

func TestYouTubeMetadataLimits(t *testing.T) {
	long := strings.Repeat("Mistério ", 30)
	if got := []rune(youtubeTitle(long)); len(got) > 100 {
		t.Errorf("title has %d runes, want <= 100", len(got))
	}
	if got := youtubeTitle("A <verdade> por trás"); strings.ContainsAny(got, "<>") {
		t.Errorf("title still contains angle brackets: %q", got)
	}
	if youtubeTitle("   ") == "" {
		t.Error("empty title must get a fallback")
	}
	if got := youtubeDescription(strings.Repeat("ção ", 3000)); len(got) > 5000 {
		t.Errorf("description has %d bytes, want <= 5000", len(got))
	}

	tags := youtubeTags(" mistério, #curiosidades ,, " + strings.Repeat("tagmuitolonga,", 80))
	if tags[0] != "mistério" || tags[1] != "curiosidades" {
		t.Errorf("tags not trimmed/cleaned: %q", tags[:2])
	}
	total := 0
	for _, tg := range tags {
		total += len([]rune(tg))
	}
	if total+len(tags) > 500 {
		t.Errorf("tag list too long for YouTube: %d", total+len(tags))
	}
}
