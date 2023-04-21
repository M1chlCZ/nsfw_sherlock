package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"io"
	"net/http"
	"os"
	"strings"
)

func GETRequest[T any](url string) (T, error) {
	var data T
	resp, err := http.Get(url)
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return data, err
	}

	if resp.StatusCode != http.StatusOK {
		if body != nil {
			err = json.Unmarshal(body, &data)
			if err != nil {
				return data, err
			}
			return data, errors.New("GET request failed with status code: " + resp.Status)
		}
		return data, errors.New("GET request failed with status code: " + resp.Status)
	}

	err = json.Unmarshal(body, &data)
	if err != nil {
		return data, err
	}

	return data, nil
}

func POSTRequest[T any](endpoint string, data *fiber.Map) (T, error) {
	var responseData T
	urlPost := fmt.Sprintf("%s%s%s", ServerUrl, "/api/v1/", endpoint)
	jsonData, err := json.Marshal(data)
	if err != nil {
		return responseData, err
	}

	req, err := http.NewRequest("POST", urlPost, bytes.NewBuffer(jsonData))
	if err != nil {
		return responseData, err
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return responseData, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return responseData, err
	}

	if resp.StatusCode != http.StatusOK {
		if body != nil {
			err = json.Unmarshal(body, &responseData)
			if err != nil {
				return responseData, err
			}
			return responseData, errors.New("GET request failed with status code: " + resp.Status)
		}
		return responseData, errors.New("GET request failed with status code: " + resp.Status)
	}

	err = json.Unmarshal(body, &responseData)
	if err != nil {
		return responseData, err
	}

	return responseData, nil
}

func DownloadImage(insciptID string) (string, error) {
	// Check if file exist in data_final folder
	if FileExists(fmt.Sprintf("%s/api/data_final/%s.webp", GetHomeDir(), insciptID[:8])) {
		return fmt.Sprintf("%s/api/data_final/%s.webp", GetHomeDir(), insciptID[:8]), nil
	}
	contentLink := fmt.Sprintf("https://ordinals.com/content/%s", insciptID)
	// Send HTTP GET request to the URL
	resp, err := http.Get(contentLink)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Extract the file format from the Content-Type header
	contentType := resp.Header.Get("Content-Type")
	dataType := strings.Split(contentType, "/")[0]
	if dataType != "image" {
		return "", errors.New("URL does not point to an image")
	}
	// Content Type: eg: image/png, decide if it is a picture or not
	format := strings.Split(contentType, "/")[1]

	// Create a new file with a unique name in the current directory
	filename := fmt.Sprintf("%s/api/data/%s.%s", GetHomeDir(), insciptID[:8], format)

	//check for file existing and return filename
	if _, err := os.Stat(filename); err == nil {
		return filename, nil
	}

	file, err := os.Create(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Copy the image data from the response body to the file
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}

	return filename, nil
}
