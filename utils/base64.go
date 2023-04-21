package utils

import (
	encoder "encoding/base64"
)

func EncodePayload(base64 []byte) string {
	//Base64 Encode
	return encoder.StdEncoding.EncodeToString(base64)
}

func DecodePayload(base64 []byte) ([]byte, error) {
	//Base64 Decode
	b64 := make([]byte, encoder.StdEncoding.DecodedLen(len(base64)))
	n, err := encoder.StdEncoding.Decode(b64, base64)
	if err != nil {
		return nil, err
	}
	return b64[:n], nil
}
