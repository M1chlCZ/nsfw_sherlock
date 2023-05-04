package common

import (
	"bufio"
	"fmt"
	"os"
)

type BadWordsMap struct {
	BadWordsMap map[string]bool
}

var BadWordsStuff *BadWordsMap

func LoadBadWords() error {
	var BadWords []string
	BadWords = make([]string, 0)
	file, err := os.Open("./bad_words.txt")
	if err != nil {
		file, err = os.Open("./bad_words_fallback.txt")
		if err != nil {
			fmt.Println("Can't open bad words file: ", err.Error())
			return err
		}
		fmt.Println("Loaded bad words from bad_words_fallback.txt")
	} else {
		fmt.Println("Loaded bad words from bad_words.txt")
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
	BadWordsStuff = &BadWordsMap{
		BadWordsMap: badWordsMap,
	}
	return nil
}

func GetBadWordsList() *BadWordsMap {
	return BadWordsStuff
}
