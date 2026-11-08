package transform

import (
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/protobuf/encoding/protowire"
)

// EncodeResource returns the encoded opentelemetry.proto.resource.v1.Resource
// message for r (attributes only).
func EncodeResource(r *resource.Resource) []byte {
	if r == nil {
		return nil
	}
	return AppendKeyValues(nil, 1, r.Attributes())
}

// AppendKeyValues appends each attribute as a length-delimited KeyValue in
// field (1 for Resource.attributes, 3 for InstrumentationScope.attributes).
func AppendKeyValues(dst []byte, field protowire.Number, kvs []attribute.KeyValue) []byte {
	for _, kv := range kvs {
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, string(kv.Key))
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, appendAttrValue(nil, kv.Value))
		dst = protowire.AppendTag(dst, field, protowire.BytesType)
		dst = protowire.AppendBytes(dst, b)
	}
	return dst
}

// appendAttrValue encodes an AnyValue body.
func appendAttrValue(b []byte, v attribute.Value) []byte {
	switch v.Type() {
	case attribute.BOOL:
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		return protowire.AppendVarint(b, protowire.EncodeBool(v.AsBool()))
	case attribute.INT64:
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		return protowire.AppendVarint(b, uint64(v.AsInt64()))
	case attribute.FLOAT64:
		b = protowire.AppendTag(b, 4, protowire.Fixed64Type)
		return protowire.AppendFixed64(b, math.Float64bits(v.AsFloat64()))
	case attribute.STRING:
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		return protowire.AppendString(b, v.AsString())
	case attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE, attribute.STRINGSLICE:
		var arr []byte
		add := func(e attribute.Value) {
			arr = protowire.AppendTag(arr, 1, protowire.BytesType)
			arr = protowire.AppendBytes(arr, appendAttrValue(nil, e))
		}
		switch v.Type() {
		case attribute.BOOLSLICE:
			for _, x := range v.AsBoolSlice() {
				add(attribute.BoolValue(x))
			}
		case attribute.INT64SLICE:
			for _, x := range v.AsInt64Slice() {
				add(attribute.Int64Value(x))
			}
		case attribute.FLOAT64SLICE:
			for _, x := range v.AsFloat64Slice() {
				add(attribute.Float64Value(x))
			}
		default:
			for _, x := range v.AsStringSlice() {
				add(attribute.StringValue(x))
			}
		}
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		return protowire.AppendBytes(b, arr)
	default:
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		return protowire.AppendString(b, v.String())
	}
}
