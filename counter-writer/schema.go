package main

// SchemaField is one declared field of the `counters` ts-store schema store.
// A schema store rejects any record field not declared here, so this list is the
// SINGLE SOURCE OF TRUTH for the contract between this writer and the deploy's
// schema-store declaration. Keep index/name/type in sync with defaultCounters().
type SchemaField struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Type  string `json:"type"` // ts-store explicit-width types: int64|int32|float64|float32|string|bool
}

// counterSchema is the record this writer emits: a timestamp plus one int64
// column per counter. The column names MUST match defaultCounters() Names — a
// GAP tick omits its field, which the schema store tolerates (missing != invalid),
// and which the dashboard reads as a null point. Indices are 1-based.
//
// ts-store has no bare "int"/"float" — counters are int64 (see traffic-writer
// schema.go). The dashboard treats these as accumulator_columns and deltas them.
var counterSchema = []SchemaField{
	{1, "timestamp", "int64"},
	{2, "bytes_total", "int64"},
	{3, "packets_total", "int64"},
	{4, "requests_total", "int64"},
	{5, "errors_total", "int64"},
	{6, "energy_wh", "int64"},
	{7, "frames_total", "int64"},
}

// validateSchema guards against defaultCounters() and the declared schema
// drifting apart. Every counter must have a matching schema field and vice
// versa (excluding the always-present timestamp). Fails loudly at startup
// rather than as opaque "field not found in schema" rejections at runtime.
func validateSchema() error {
	declared := make(map[string]bool, len(counterSchema))
	for _, f := range counterSchema {
		declared[f.Name] = true
	}
	// Every counter column must be declared.
	for _, c := range counters {
		if !declared[c.Name] {
			return &schemaError{c.Name}
		}
	}
	// Every declared field (besides timestamp) must back a counter.
	have := map[string]bool{"timestamp": true}
	for _, c := range counters {
		have[c.Name] = true
	}
	for name := range declared {
		if !have[name] {
			return &schemaError{name}
		}
	}
	return nil
}

type schemaError struct{ field string }

func (e *schemaError) Error() string {
	return "schema/counter mismatch on field: " + e.field
}
