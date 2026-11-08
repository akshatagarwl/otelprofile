package oracle

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Resource converts an SDK resource to its OTLP form.
func Resource(r *resource.Resource) *resourcepb.Resource {
	if r == nil {
		return &resourcepb.Resource{}
	}
	out := &resourcepb.Resource{}
	for _, kv := range r.Attributes() {
		out.Attributes = append(out.Attributes, KeyValue(kv))
	}
	return out
}

// Scope converts an instrumentation scope to its OTLP form.
func Scope(s instrumentation.Scope) *commonpb.InstrumentationScope {
	out := &commonpb.InstrumentationScope{Name: s.Name, Version: s.Version}
	for _, kv := range s.Attributes.ToSlice() {
		out.Attributes = append(out.Attributes, KeyValue(kv))
	}
	return out
}

// KeyValue converts one attribute.
func KeyValue(kv attribute.KeyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: string(kv.Key), Value: anyValue(kv.Value)}
}

func anyValue(v attribute.Value) *commonpb.AnyValue {
	switch v.Type() {
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v.AsBool()}}
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v.AsInt64()}}
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v.AsFloat64()}}
	case attribute.STRING:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.AsString()}}
	case attribute.BOOLSLICE:
		var vals []*commonpb.AnyValue
		for _, b := range v.AsBoolSlice() {
			vals = append(vals, &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: b}})
		}
		return arr(vals)
	case attribute.INT64SLICE:
		var vals []*commonpb.AnyValue
		for _, i := range v.AsInt64Slice() {
			vals = append(vals, &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: i}})
		}
		return arr(vals)
	case attribute.FLOAT64SLICE:
		var vals []*commonpb.AnyValue
		for _, f := range v.AsFloat64Slice() {
			vals = append(vals, &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: f}})
		}
		return arr(vals)
	case attribute.STRINGSLICE:
		var vals []*commonpb.AnyValue
		for _, s := range v.AsStringSlice() {
			vals = append(vals, &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}})
		}
		return arr(vals)
	default:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.String()}}
	}
}

func arr(vals []*commonpb.AnyValue) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: vals}}}
}
