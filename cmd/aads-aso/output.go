package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

func printOutput(data any) error {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "table":
		return printTable(os.Stdout, data)
	case "yaml":
		b, err := yaml.Marshal(data)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	default:
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(b, '\n'))
		return err
	}
}

func printRawJSON(data []byte) error {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		_, _ = os.Stdout.Write(data)
		fmt.Fprintln(os.Stdout)
		return nil
	}
	return printOutput(parsed)
}

func printTable(w io.Writer, data any) error {
	v := reflect.ValueOf(data)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()

	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		if v.Len() == 0 {
			return nil
		}
		first := indirectValue(v.Index(0))
		if first.Kind() == reflect.Struct {
			headers := structHeaders(first.Type())
			fmt.Fprintln(tw, strings.Join(headers, "\t"))
			for i := 0; i < v.Len(); i++ {
				row := indirectValue(v.Index(i))
				values := structValues(row, headers)
				fmt.Fprintln(tw, strings.Join(values, "\t"))
			}
			return nil
		}
		// Fallback: print each element as JSON.
		for i := 0; i < v.Len(); i++ {
			b, _ := json.Marshal(v.Index(i).Interface())
			fmt.Fprintln(tw, string(b))
		}
		return nil
	case reflect.Struct:
		headers := structHeaders(v.Type())
		values := structValues(v, headers)
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
		fmt.Fprintln(tw, strings.Join(values, "\t"))
		return nil
	default:
		b, _ := json.Marshal(data)
		fmt.Fprintln(tw, string(b))
		return nil
	}
}

func indirectValue(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return v
		}
		v = v.Elem()
	}
	return v
}

func structHeaders(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		if name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func structValues(v reflect.Value, headers []string) []string {
	t := v.Type()
	indexByHeader := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		if name == "-" {
			continue
		}
		indexByHeader[name] = i
	}

	out := make([]string, 0, len(headers))
	for _, h := range headers {
		i, ok := indexByHeader[h]
		if !ok {
			out = append(out, "")
			continue
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				out = append(out, "")
				continue
			}
			fv = fv.Elem()
		}
		out = append(out, fmt.Sprint(fv.Interface()))
	}
	return out
}
