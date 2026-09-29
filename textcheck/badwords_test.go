package textcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func loadEmbedded(t *testing.T) {
	t.Helper()
	if err := LoadBadWords(""); err != nil {
		t.Fatalf("LoadBadWords(\"\") error = %v, want nil", err)
	}
}

func TestContainsBadWords(t *testing.T) {
	loadEmbedded(t)

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "exact match", input: "fuck", want: []string{"fuck"}},
		{name: "case insensitive", input: "FuCk", want: []string{"fuck"}},
		{name: "OCR whitespace separates words", input: "hello\nfuck\tshit\rworld", want: []string{"fuck", "shit"}},
		{name: "punctuation stripped", input: "f.u.c.k", want: []string{"fuck"}},
		{name: "digits stripped", input: "fuck123", want: []string{"fuck"}},
		{name: "unicode letters dropped", input: "föck", want: nil},
		{name: "multiple occurrences in input order", input: "fuck shit fuck", want: []string{"fuck", "shit", "fuck"}},
		{name: "unknown words", input: "hello world", want: nil},
		{name: "empty input", input: "", want: nil},
		{name: "whitespace only input", input: " \t\n ", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsBadWords(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ContainsBadWords(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoadBadWordsOverride(t *testing.T) {
	dir := t.TempDir()

	t.Run("custom file replaces embedded list", func(t *testing.T) {
		path := filepath.Join(dir, "bad_words.txt")
		if err := os.WriteFile(path, []byte("zzznotaword\nqqqphrase\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := LoadBadWords(path); err != nil {
			t.Fatalf("LoadBadWords(%q) error = %v, want nil", path, err)
		}

		got := ContainsBadWords("zzznotaword qqqphrase")
		want := []string{"zzznotaword", "qqqphrase"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ContainsBadWords(custom words) = %#v, want %#v", got, want)
		}
		if got := ContainsBadWords("fuck"); got != nil {
			t.Errorf("ContainsBadWords(fuck) = %#v, want nil after override", got)
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		if err := LoadBadWords(filepath.Join(dir, "missing.txt")); err == nil {
			t.Fatal("LoadBadWords(missing file) error = nil, want error")
		}
	})

	t.Run("empty file returns error", func(t *testing.T) {
		path := filepath.Join(dir, "empty.txt")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := LoadBadWords(path); err == nil {
			t.Fatal("LoadBadWords(empty file) error = nil, want error")
		}
	})

	t.Run("empty path loads embedded fallback", func(t *testing.T) {
		loadEmbedded(t)
		if got := ContainsBadWords("fuck"); !reflect.DeepEqual(got, []string{"fuck"}) {
			t.Errorf("ContainsBadWords(fuck) = %#v, want [fuck]", got)
		}
	})
}

func TestLoadBadWordsReloadRace(t *testing.T) {
	dir := t.TempDir()
	listA := filepath.Join(dir, "a.txt")
	listB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(listA, []byte("alphaonly\nalphaone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listB, []byte("betaonly\nbetaone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadBadWords(listA); err != nil {
		t.Fatalf("LoadBadWords(listA) error = %v, want nil", err)
	}

	wantA := []string{"alphaonly", "alphaone"}
	wantB := []string{"betaonly", "betaone"}
	const input = "alphaonly alphaone betaonly betaone"

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				got := ContainsBadWords(input)
				if !reflect.DeepEqual(got, wantA) && !reflect.DeepEqual(got, wantB) {
					t.Errorf("ContainsBadWords saw torn reload state: %#v", got)
					return
				}
			}
		})
	}

	for i := range 200 {
		path := listA
		if i%2 == 0 {
			path = listB
		}
		if err := LoadBadWords(path); err != nil {
			t.Errorf("LoadBadWords(%q) error = %v, want nil", path, err)
			break
		}
	}
	close(stop)
	readers.Wait()
}

func TestLoadBadWordsReload(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(first, []byte("alphaonly\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("betaonly\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadBadWords(first); err != nil {
		t.Fatalf("LoadBadWords(first) error = %v, want nil", err)
	}
	if got := ContainsBadWords("alphaonly betaonly"); !reflect.DeepEqual(got, []string{"alphaonly"}) {
		t.Errorf("after first load: ContainsBadWords = %#v, want [alphaonly]", got)
	}

	if err := LoadBadWords(second); err != nil {
		t.Fatalf("LoadBadWords(second) error = %v, want nil", err)
	}
	if got := ContainsBadWords("alphaonly betaonly"); !reflect.DeepEqual(got, []string{"betaonly"}) {
		t.Errorf("after second load: ContainsBadWords = %#v, want [betaonly]", got)
	}
}

func TestLoaded(t *testing.T) {
	badWords.Store(nil)
	if Loaded() {
		t.Fatal("Loaded() = true before any successful load, want false")
	}

	if err := LoadBadWords(""); err != nil {
		t.Fatalf("LoadBadWords(\"\") error = %v, want nil", err)
	}
	if !Loaded() {
		t.Fatal("Loaded() = false after successful load, want true")
	}

	if err := LoadBadWords(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Fatal("LoadBadWords(missing file) error = nil, want error")
	}
	if !Loaded() {
		t.Error("Loaded() = false after failed load, want true (previous list kept)")
	}
	if got := ContainsBadWords("fuck"); !reflect.DeepEqual(got, []string{"fuck"}) {
		t.Errorf("ContainsBadWords(fuck) after failed load = %#v, want [fuck] (previous list kept)", got)
	}
}

func TestContainsBadWordsConcurrent(t *testing.T) {
	loadEmbedded(t)

	const goroutines = 8
	const iterations = 200

	want := []string{"fuck", "shit"}
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range iterations {
				if got := ContainsBadWords("fuck shit"); !reflect.DeepEqual(got, want) {
					t.Errorf("ContainsBadWords = %#v, want %#v", got, want)
					return
				}
			}
		})
	}
	wg.Wait()
}
