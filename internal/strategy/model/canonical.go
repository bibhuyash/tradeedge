package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

var ErrInvalidCanonicalJSON = errors.New("invalid canonical JSON")

func canonicalJSONObject(raw []byte, maximum int) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return nil, ErrInvalidCanonicalJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrInvalidCanonicalJSON
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidCanonicalJSON
	}
	var output bytes.Buffer
	if err := encodeCanonicalJSON(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrInvalidCanonicalJSON
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			result := make(map[string]any)
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return nil, ErrInvalidCanonicalJSON
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, ErrInvalidCanonicalJSON
				}
				if _, exists := result[key]; exists {
					return nil, ErrInvalidCanonicalJSON
				}
				child, childErr := decodeJSONValue(decoder)
				if childErr != nil {
					return nil, childErr
				}
				result[key] = child
			}
			if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
				return nil, ErrInvalidCanonicalJSON
			}
			return result, nil
		case '[':
			var result []any
			for decoder.More() {
				child, childErr := decodeJSONValue(decoder)
				if childErr != nil {
					return nil, childErr
				}
				result = append(result, child)
			}
			if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim(']') {
				return nil, ErrInvalidCanonicalJSON
			}
			return result, nil
		default:
			return nil, ErrInvalidCanonicalJSON
		}
	case json.Number:
		text := string(value)
		if !validIntegerJSON(text) {
			return nil, ErrInvalidCanonicalJSON
		}
		return value, nil
	case string, bool, nil:
		return value, nil
	default:
		return nil, ErrInvalidCanonicalJSON
	}
}

func validIntegerJSON(value string) bool {
	if value == "0" {
		return true
	}
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = strings.TrimPrefix(value, "-")
	}
	if value == "" || value[0] == '0' || (negative && value == "0") {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func encodeCanonicalJSON(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key)
			output.Write(encodedKey)
			output.WriteByte(':')
			if err := encodeCanonicalJSON(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	case []any:
		output.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := encodeCanonicalJSON(output, child); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case string:
		encoded, _ := json.Marshal(typed)
		output.Write(encoded)
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case nil:
		output.WriteString("null")
	case json.Number:
		output.WriteString(string(typed))
	default:
		return ErrInvalidCanonicalJSON
	}
	return nil
}
