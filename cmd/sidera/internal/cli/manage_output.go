package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/aianyai/sidera/internal/node/apiwire"
)

func managementObject(raw []byte) (map[string]any, error) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, apiwire.OutcomeUnknown("")
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Result))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, apiwire.OutcomeUnknown("")
	}
	return result, nil
}

func (c *command) managementResult(raw []byte) error {
	if c.json {
		_, err := c.Stdout.Write(raw)
		return err
	}
	result, err := managementObject(raw)
	if err != nil {
		return err
	}
	if reminders, ok := result["reminders"].([]any); ok {
		for _, entry := range reminders {
			if reminder, ok := entry.(map[string]any); ok {
				if _, err := fmt.Fprintln(c.Stdout, reminder["message"]); err != nil {
					return err
				}
			}
		}
		delete(result, "reminders")
	}
	w := tabwriter.NewWriter(c.Stdout, 0, 2, 2, ' ', 0)
	if err := humanFields(w, "", result); err != nil {
		return err
	}
	var metadata struct {
		OperationID string `json:"operation_id"`
		Replayed    bool   `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	if metadata.OperationID != "" {
		fmt.Fprintf(w, "operation_id %s\nreplayed %t\n", metadata.OperationID, metadata.Replayed)
	}
	return w.Flush()
}

func humanFields(w io.Writer, prefix string, object map[string]any) error {
	for _, key := range sortedKeys(object) {
		name := prefix + key
		switch value := object[key].(type) {
		case map[string]any:
			if err := humanFields(w, name+".", value); err != nil {
				return err
			}
		case []any:
			if err := humanList(w, name, value); err != nil {
				return err
			}
		default:
			text, err := humanValue(value)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%s %s\n", name, text); err != nil {
				return err
			}
		}
	}
	return nil
}

func humanList(w io.Writer, name string, values []any) error {
	if len(values) == 0 {
		_, err := fmt.Fprintf(w, "%s 0\n", name)
		return err
	}
	columns := map[string]any{}
	for _, entry := range values {
		row, ok := entry.(map[string]any)
		if !ok {
			text, err := jsonText(values)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(w, "%s %s\n", name, text)
			return err
		}
		for key := range row {
			columns[key] = nil
		}
	}
	keys := sortedKeys(columns)
	if _, err := fmt.Fprintln(w, name); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, strings.ToUpper(strings.Join(keys, "\t"))); err != nil {
		return err
	}
	for _, entry := range values {
		row := entry.(map[string]any)
		cells := make([]string, len(keys))
		for i, key := range keys {
			if value, ok := row[key]; ok {
				cell, err := humanValue(value)
				if err != nil {
					return err
				}
				cells[i] = cell
			}
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func humanValue(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	return jsonText(value)
}
