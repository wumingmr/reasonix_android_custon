package session

import (
	"reflect"

	"reasonix/internal/provider"
)

func detachMessages(messages []provider.Message) []provider.Message {
	if messages == nil {
		return []provider.Message{}
	}
	return detachValue(reflect.ValueOf(messages)).Interface().([]provider.Message)
}

// Provider DTOs contain nested slices, pointers, maps and raw JSON. Copy their
// mutable containers, preserving every field without a lossy shadow schema.
func detachValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		inner := detachValue(value.Elem())
		out := reflect.New(value.Type()).Elem()
		if value.Kind() == reflect.Pointer {
			out.Set(reflect.New(value.Type().Elem()))
			out.Elem().Set(inner)
		} else {
			out.Set(inner)
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := range value.Len() {
			out.Index(i).Set(detachValue(value.Index(i)))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), detachValue(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := range value.NumField() {
			if out.Field(i).CanSet() && value.Field(i).CanInterface() {
				out.Field(i).Set(detachValue(value.Field(i)))
			}
		}
		return out
	default:
		return value
	}
}
