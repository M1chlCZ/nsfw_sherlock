package common

import (
	"bufio"
	"os"
)

var BadWordsMap map[string]bool

func LoadBadWords() error {
	var BadWords []string
	BadWords = make([]string, 0)
	file, err := os.Open("./bad_words.txt")
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		BadWords = append(BadWords, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	badWordsMap := make(map[string]bool)
	for _, badWord := range BadWords {
		badWordsMap[badWord] = true
	}
	return nil
}
