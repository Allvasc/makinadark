package services

import (
	"strings"
	"testing"
)

func TestSplitTextIntoSubtitleChunks(t *testing.T) {
	sampleText := "Bem-vindos ao Mysterium, onde o desconhecido se torna fascinante! Aqui, mergulhamos nos mais intrigantes enigmas e curiosidades que desafiam nossa compreensão do mundo."

	chunks := splitTextIntoSubtitleChunks(sampleText)

	if len(chunks) == 0 {
		t.Fatalf("expected non-empty chunks, got 0")
	}

	for i, chunk := range chunks {
		words := strings.Fields(chunk)
		if len(words) > 6 {
			t.Errorf("chunk %d has %d words, exceeding limit of 6: %q", i, len(words), chunk)
		}
		if len([]rune(chunk)) > 45 {
			t.Errorf("chunk %d has %d characters, exceeding max limit: %q", i, len([]rune(chunk)), chunk)
		}
	}
}

func TestAppendSubtitleEntries(t *testing.T) {
	var subtitleLines []string
	subtitleIdx := 0
	sampleText := "Bem-vindos ao Mysterium, onde o desconhecido se torna fascinante!"

	finalTime := appendSubtitleEntries(&subtitleLines, sampleText, 0.0, 10.0, &subtitleIdx)

	if finalTime != 10.0 {
		t.Errorf("expected final time 10.0, got %f", finalTime)
	}
	if len(subtitleLines) < 2 {
		t.Errorf("expected multiple subtitle entries for text, got %d", len(subtitleLines))
	}
}
