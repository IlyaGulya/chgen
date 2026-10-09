package coverage

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// encoding/json otherwise keeps the last repeated key. Evidence must have a
// single interpretation, including nested stage and IR objects.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return visitJSONValue(decoder, 0)
}

func visitJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 256 {
		return fmt.Errorf("evidence JSON exceeds 256 nesting levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return fmt.Errorf("evidence JSON has an invalid or repeated key %q", key)
			}
			seen[key] = true
			if err := visitJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case json.Delim('['):
		for decoder.More() {
			if err := visitJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}
