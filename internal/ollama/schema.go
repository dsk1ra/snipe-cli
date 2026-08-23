package ollama

import (
	"bytes"
	"encoding/json"
)

// Schema is a JSON Schema with its property order preserved.
//
// Order is not cosmetic here. Ollama compiles `format` into a decoding grammar,
// and the grammar emits object properties in the order the schema lists them —
// so the order decides what context each field is generated against. A schema
// reordered alphabetically produces different prose from the same model, prompt
// and temperature.
//
// That is exactly what Go's encoding/json does to a map[string]any: it sorts
// the keys. Scoring the same JD through the Node implementation and a
// map-based port gave identical dimension scores and visibly different
// soft_gaps and top_strengths, which is a silent quality change with nothing to
// notice it. Hence this type.
type Schema []Field

type Field struct {
	Key   string
	Value any
}

func (s Schema) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range s {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Obj is shorthand for a nested object in a schema.
func Obj(fields ...Field) Schema { return Schema(fields) }

// F is shorthand for one field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// Int is an integer property constrained to a range.
func Int(min, max int) Schema {
	return Obj(F("type", "integer"), F("minimum", min), F("maximum", max))
}

// Str is a string property, optionally constrained to an enum.
func Str(enum ...string) Schema {
	if len(enum) == 0 {
		return Obj(F("type", "string"))
	}
	return Obj(F("type", "string"), F("enum", enum))
}

// Arr is an array property with an item schema and an optional cap.
func Arr(items any, maxItems int) Schema {
	s := Obj(F("type", "array"))
	if maxItems > 0 {
		s = append(s, F("maxItems", maxItems))
	}
	return append(s, F("items", items))
}

// Object assembles an object schema from ordered properties plus the required
// list, which is every named property unless overridden.
func Object(props Schema, required ...string) Schema {
	if len(required) == 0 {
		for _, f := range props {
			required = append(required, f.Key)
		}
	}
	return Obj(
		F("type", "object"),
		F("properties", props),
		F("required", required),
	)
}
