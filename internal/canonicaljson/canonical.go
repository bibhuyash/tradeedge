package canonicaljson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("invalid canonical JSON")

type Limits struct {
	MaximumBytes      int
	MaximumDepth      int
	MaximumCollection int
}

func Object(raw []byte, maximumBytes int) ([]byte, error) {
	return ObjectBounded(raw, Limits{MaximumBytes: maximumBytes})
}

func ObjectBounded(raw []byte, limits Limits) ([]byte, error) {
	if len(raw) == 0 || limits.MaximumBytes <= 0 || len(raw) > limits.MaximumBytes {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeValue(decoder, limits, 1)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	var output bytes.Buffer
	if err := encode(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeValue(decoder *json.Decoder, limits Limits, depth int) (any, error) {
	if limits.MaximumDepth > 0 && depth > limits.MaximumDepth {
		return nil, ErrInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrInvalid
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			result := make(map[string]any)
			for decoder.More() {
				if limits.MaximumCollection > 0 && len(result) >= limits.MaximumCollection {
					return nil, ErrInvalid
				}
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return nil, ErrInvalid
				}
				key, ok := keyToken.(string)
				if !ok || key == "" {
					return nil, ErrInvalid
				}
				if _, exists := result[key]; exists {
					return nil, ErrInvalid
				}
				child, childErr := decodeValue(decoder, limits, depth+1)
				if childErr != nil {
					return nil, childErr
				}
				result[key] = child
			}
			if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
				return nil, ErrInvalid
			}
			return result, nil
		case '[':
			var result []any
			for decoder.More() {
				if limits.MaximumCollection > 0 && len(result) >= limits.MaximumCollection {
					return nil, ErrInvalid
				}
				child, childErr := decodeValue(decoder, limits, depth+1)
				if childErr != nil {
					return nil, childErr
				}
				result = append(result, child)
			}
			if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim(']') {
				return nil, ErrInvalid
			}
			return result, nil
		default:
			return nil, ErrInvalid
		}
	case json.Number:
		if !validInteger(string(value)) {
			return nil, ErrInvalid
		}
		return value, nil
	case string, bool, nil:
		return value, nil
	default:
		return nil, ErrInvalid
	}
}

func validInteger(value string) bool {
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

func encode(output *bytes.Buffer, value any) error {
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
			if err := encode(output, typed[key]); err != nil {
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
			if err := encode(output, child); err != nil {
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
		return ErrInvalid
	}
	return nil
}
