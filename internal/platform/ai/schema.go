// schema.go validates the caller's schema subset and checks an answer
// against it. Both stubbed until AIR-02-03 implements Call's real path.
package ai

import (
	"encoding/json"
	"errors"
)

func checkSchema(raw json.RawMessage) (map[string]any, error) {
	return nil, errors.New("ai: not implemented")
}

func checkAnswer(schema map[string]any, v any) error {
	return errors.New("ai: not implemented")
}
