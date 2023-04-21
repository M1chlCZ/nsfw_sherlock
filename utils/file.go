package utils

import (
	"encoding/base64"
	"io"
	"os"
	"strings"
)

func FileSizeInBytes(filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}

	return stat.Size(), nil
}

func ReadFileAsBytes(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func ReadFileAsBase64(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Create a buffer to store the encoded data
	var encodedData strings.Builder

	// Create a new base64 encoder that writes to the buffer
	encoder := base64.NewEncoder(base64.StdEncoding, &encodedData)

	// Stream the file through the encoder
	_, err = io.Copy(encoder, file)
	if err != nil {
		return "", err
	}

	// Close the encoder to flush any remaining bytes
	err = encoder.Close()
	if err != nil {
		return "", err
	}

	return encodedData.String(), nil
}
