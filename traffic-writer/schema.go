package main

// SchemaField is one declared field of the `traffic-flows` ts-store schema store.
// A schema store rejects any record field not declared here, so this list is the
// SINGLE SOURCE OF TRUTH for the contract between this writer and the trv-homelab
// Ansible role's `simulators_tsstore_schema_stores` block. Keep index/name/type
// in sync; see docs/TRAFFIC-SIM-PLAN.md ("Coupled change").
type SchemaField struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Type  string `json:"type"` // ts-store schema types: int | float | string
}

// trafficFlowSchema is the aggregated-flow record the ts-store writer emits.
// The order here defines the indices. Mirrors the Flow JSON in data.go plus the
// write-time `timestamp`. The Ansible role must declare exactly these fields.
var trafficFlowSchema = []SchemaField{
	{0, "timestamp", "int"},
	{1, "country", "string"},
	{2, "cc", "string"},
	{3, "src_lat", "float"},
	{4, "src_lon", "float"},
	{5, "region", "string"},
	{6, "dst_lat", "float"},
	{7, "dst_lon", "float"},
	{8, "count", "int"},
}

// flowRecord builds the ts-store data payload for one flow, keyed by the schema
// field names. Driven by the same field set the schema declares — if a name here
// is not in trafficFlowSchema, validateSchema() fails fast at startup.
func flowRecord(f Flow, timestampMs int64) map[string]interface{} {
	return map[string]interface{}{
		"timestamp": timestampMs,
		"country":   f.Country,
		"cc":        f.CC,
		"src_lat":   f.SrcLat,
		"src_lon":   f.SrcLon,
		"region":    f.Region,
		"dst_lat":   f.DstLat,
		"dst_lon":   f.DstLon,
		"count":     f.Count,
	}
}

// validateSchema guards against the record builder and the declared schema
// drifting apart. Called at startup so a mismatch fails loudly here rather than
// as opaque "field not found in schema" rejections from ts-store at runtime.
func validateSchema() error {
	declared := make(map[string]bool, len(trafficFlowSchema))
	for _, f := range trafficFlowSchema {
		declared[f.Name] = true
	}
	sample := flowRecord(Flow{}, 0)
	for name := range sample {
		if !declared[name] {
			return &schemaError{name}
		}
	}
	for name := range declared {
		if _, ok := sample[name]; !ok {
			return &schemaError{name}
		}
	}
	return nil
}

type schemaError struct{ field string }

func (e *schemaError) Error() string {
	return "schema/record mismatch on field: " + e.field
}
