package otellog

import (
	"context"
	"encoding"
	"fmt"
	"log/slog"
	"reflect"
)

// plainAttrs turns attribute values the OTel bridge cannot map into the slog
// kind that carries their value. The bridge renders any KindAny value it does
// not know as "unhandled: (pkg.Type) value", so a named string type or an
// enum reaches the log store with that prefix and cannot be filtered on.
// Errors are left alone: the bridge maps them itself.
type plainAttrs struct{ slog.Handler }

func (h *plainAttrs) Handle(ctx context.Context, record slog.Record) error {
	converted := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(a slog.Attr) bool {
		converted.AddAttrs(plainAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, converted)
}

func (h *plainAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	plain := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		plain[i] = plainAttr(a)
	}
	return &plainAttrs{Handler: h.Handler.WithAttrs(plain)}
}

func (h *plainAttrs) WithGroup(name string) slog.Handler {
	return &plainAttrs{Handler: h.Handler.WithGroup(name)}
}

func plainAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindGroup:
		group := a.Value.Group()
		plain := make([]slog.Attr, len(group))
		for i, member := range group {
			plain[i] = plainAttr(member)
		}
		a.Value = slog.GroupValue(plain...)
	case slog.KindAny:
		a.Value = plainValue(a.Value.Any())
	}
	return a
}

func plainValue(v any) slog.Value {
	switch t := v.(type) {
	case nil, error:
		return slog.AnyValue(v)
	case fmt.Stringer:
		return slog.StringValue(safeString(t.String))
	case encoding.TextMarshaler:
		return slog.StringValue(safeString(func() string {
			text, err := t.MarshalText()
			if err != nil {
				return fmt.Sprintf("!ERROR:%v", err)
			}
			return string(text)
		}))
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return slog.StringValue(rv.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return slog.Int64Value(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return slog.Uint64Value(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return slog.Float64Value(rv.Float())
	case reflect.Bool:
		return slog.BoolValue(rv.Bool())
	}
	return slog.AnyValue(v)
}

// safeString calls a Stringer or marshaler that a logging call must not be
// brought down by: a nil pointer receiver panics in the same way fmt guards
// against.
func safeString(f func() string) (s string) {
	defer func() {
		if r := recover(); r != nil {
			s = fmt.Sprintf("!PANIC:%v", r)
		}
	}()
	return f()
}
