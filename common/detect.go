package common

import (
	"fmt"
	"github.com/otiai10/gosseract/v2"
	"log"
	"nsfw_sherlock/nsfw"
	"nsfw_sherlock/utils"
	"path/filepath"
	"strings"
)

var modelPath, _ = filepath.Abs("./assets/nsfw")
var detector = nsfw.New(modelPath)

func TestPictureNSFW(filename string) (bool, error) {
	l, err := detect(filename)
	if err != nil {
		return false, err
	}
	return l.IsNSFW(), nil
}

func detect(filename string) (nsfw.Labels, error) {
	result, err := detector.File(filename)
	if err != nil {
		log.Fatalln(err.Error())
		return result, err
	}

	return result, nil
}

func DetectTextNSFW(filename string) (bool, error) {
	client := gosseract.NewClient()
	defer client.Close()
	err := client.SetImage(filename)
	if err != nil {
		return false, err
	}
	text, _ := client.Text()

	if len(text) == 0 {
		return false, nil
	}
	cw := containsBadWords(text)
	if len(cw) > 0 {
		utils.ReportSuccess(fmt.Sprint("Bad words found:", strings.Join(cw, ", ")))
		return true, nil
	}
	return false, nil
}

func containsBadWords(text string) []string {
	// Convert the text to lowercase
	lowerText := strings.ToLower(text)

	// Remove punctuation and special characters
	filteredText := strings.Map(func(r rune) rune {
		if strings.ContainsRune("abcdefghijklmnopqrstuvwxyz ", r) {
			return r
		}
		return -1
	}, lowerText)

	// Split the text into words
	words := strings.Fields(filteredText)

	// Create a map of bad words for faster lookups
	badWordsMap := make(map[string]bool)
	for _, badWord := range BadWords {
		badWordsMap[badWord] = true
	}

	// Compare the words with the list of bad words
	var badWordsFound []string
	for _, word := range words {
		utils.ReportSuccess("Checking word: " + word)
		if badWordsMap[word] {
			utils.ReportMessage("Bad word found: " + word)
			badWordsFound = append(badWordsFound, word)
		}
	}

	return badWordsFound
}
