package textcheck

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"unicode"
)

//go:embed bad_words_fallback.txt
var embeddedFallback []byte

var badWords atomic.Pointer[map[string]bool]

// LoadBadWords replaces the active bad-word list.
func LoadBadWords(path string) error {
	data := embeddedFallback
	if path != "" {
		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("textcheck: cannot read bad words file %q: %w", path, err)
		}
		if len(fileBytes) == 0 {
			return fmt.Errorf("textcheck: bad words file %q is empty", path)
		}
		data = fileBytes
	}

	list := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		list[scanner.Text()] = true
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("textcheck: cannot parse bad words file %q: %w", path, err)
	}

	badWords.Store(&list)
	return nil
}

// Loaded reports whether a successful LoadBadWords call has installed a bad-word list.
func Loaded() bool {
	return badWords.Load() != nil
}

// ContainsBadWords returns the bad words found in text, in input order and with duplicates preserved.
func ContainsBadWords(text string) []string {
	lowerText := strings.ToLower(text)
	filteredText := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, lowerText)
	words := strings.Fields(filteredText)
	if len(words) == 0 {
		return nil
	}

	list := badWords.Load()
	if list == nil {
		return nil
	}

	var found []string
	for _, word := range words {
		if (*list)[word] {
			found = append(found, word)
		}
	}
	return found
}
