// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package env

import (
	"fmt"
	"io"
	"os"
	"reflect"
)

// Write writes an environment file with the given name and content.
func Write(name string, e any) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer f.Close()
	if err := marshalEnv(f, e); err != nil {
		return fmt.Errorf("failed to marshal env: %v", err)
	}
	return f.Close()
}

func marshalEnv(o io.Writer, e any) error {
	re := reflect.ValueOf(e)
	if re.Kind() == reflect.Ptr {
		re = re.Elem()
	}
	ret := re.Type()
	for i := 0; i < re.NumField(); i++ {
		field := re.Field(i)
		tag := ret.Field(i).Tag.Get("env")
		if tag == "" {
			continue
		}
		if field.IsZero() {
			continue
		}
		fmt.Fprintf(o, "%s=%s\n", tag, field.Interface())
	}
	return nil
}
