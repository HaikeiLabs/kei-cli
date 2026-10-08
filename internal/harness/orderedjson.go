package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// orderedObject is a JSON object that preserves key insertion order. The
// standard library marshals map[string]any with keys sorted alphabetically,
// which would reorder a user's hand-written settings file. This type keeps
// the original key order so `kei harness sync` only changes the Kei-managed
// entries and leaves the rest of the file byte-stable.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func (o *orderedObject) get(key string) (any, bool) {
	v, ok := o.values[key]
	return v, ok
}

// set stores a value, appending the key to the order list only the first time
// it is seen so re-setting an existing key does not move it.
func (o *orderedObject) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// decodeOrdered reads a single JSON value from dec, preserving object key
// order at every nesting level.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &orderedObject{values: map[string]any{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("expected object key string, got %v", keyTok)
				}
				val, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, val)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", t)
		}
	case nil:
		return nil, nil
	default:
		return t, nil
	}
}

// parseOrdered parses a JSON document into an order-preserving structure.
// Numbers are kept as json.Number so their original literal form survives a
// round trip.
func parseOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return decodeOrdered(dec)
}

// marshalOrdered marshals an order-preserving structure to indented JSON.
// It mirrors json.MarshalIndent's layout (two-space indent, a space after the
// colon) but preserves key order and does not escape <, >, or & (the
// SetEscapeHTML(false) behavior).
func marshalOrdered(v any, indent string) ([]byte, error) {
	return marshalOrderedAt(v, indent, 0)
}

func marshalOrderedAt(v any, indent string, depth int) ([]byte, error) {
	switch t := v.(type) {
	case *orderedObject:
		if len(t.keys) == 0 {
			return []byte("{}"), nil
		}
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, key := range t.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
			buf.WriteString(strings.Repeat(indent, depth+1))
			keyBytes, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			buf.Write(keyBytes)
			buf.WriteString(": ")
			valBytes, err := marshalOrderedAt(t.values[key], indent, depth+1)
			if err != nil {
				return nil, err
			}
			buf.Write(valBytes)
		}
		buf.WriteByte('\n')
		buf.WriteString(strings.Repeat(indent, depth))
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case []any:
		if len(t) == 0 {
			return []byte("[]"), nil
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, elem := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
			buf.WriteString(strings.Repeat(indent, depth+1))
			valBytes, err := marshalOrderedAt(elem, indent, depth+1)
			if err != nil {
				return nil, err
			}
			buf.Write(valBytes)
		}
		buf.WriteByte('\n')
		buf.WriteString(strings.Repeat(indent, depth))
		buf.WriteByte(']')
		return buf.Bytes(), nil
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(t); err != nil {
			return nil, err
		}
		return bytes.TrimRight(buf.Bytes(), "\n"), nil
	}
}
