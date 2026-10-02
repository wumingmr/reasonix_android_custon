package provider

import (
	"errors"
	"reflect"

	"golang.org/x/net/http2"
)

// HTTP2TransportCode classifies typed transport evidence without authorizing
// retries. API response bodies and arbitrary error wording are not evidence.
func HTTP2TransportCode(err error) string {
	var response *APIError
	if errors.As(err, &response) {
		return ""
	}
	var connection http2.ConnectionError
	var stream http2.StreamError
	var goAway http2.GoAwayError
	var connectionPtr *http2.ConnectionError
	var streamPtr *http2.StreamError
	var goAwayPtr *http2.GoAwayError
	switch {
	case errors.As(err, &connection):
		return http2CodeName(uint64(connection))
	case errors.As(err, &stream):
		return http2CodeName(uint64(stream.Code))
	case errors.As(err, &goAway):
		return http2CodeName(uint64(goAway.ErrCode))
	case errors.As(err, &connectionPtr) && connectionPtr != nil:
		return http2CodeName(uint64(*connectionPtr))
	case errors.As(err, &streamPtr) && streamPtr != nil:
		return http2CodeName(uint64(streamPtr.Code))
	case errors.As(err, &goAwayPtr) && goAwayPtr != nil:
		return http2CodeName(uint64(goAwayPtr.ErrCode))
	default:
		return bundledHTTP2Code(err)
	}
}

func http2CodeName(code uint64) string {
	if code > uint64(http2.ErrCodeHTTP11Required) {
		return ""
	}
	return http2.ErrCode(code).String()
}

// net/http bundles private equivalents of x/net's types. Inspect only those
// exact package/type identities and uint32 fields; unknown layouts fail closed.
// Real TLS fixtures guard this adapter against Go toolchain changes.
func bundledHTTP2Code(err error) string {
	if err == nil {
		return ""
	}
	v := reflect.ValueOf(err)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Type().PkgPath() == "net/http" {
		switch v.Type().Name() {
		case "http2ConnectionError":
			if v.Kind() == reflect.Uint32 {
				return http2CodeName(v.Uint())
			}
		case "http2StreamError":
			return bundledHTTP2Field(v, "Code")
		case "http2GoAwayError":
			return bundledHTTP2Field(v, "ErrCode")
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if code := bundledHTTP2Code(cause); code != "" {
				return code
			}
		}
		return ""
	}
	return bundledHTTP2Code(errors.Unwrap(err))
}

func bundledHTTP2Field(v reflect.Value, name string) string {
	if v.Kind() != reflect.Struct {
		return ""
	}
	field := v.FieldByName(name)
	if field.IsValid() && field.Kind() == reflect.Uint32 {
		return http2CodeName(field.Uint())
	}
	return ""
}
