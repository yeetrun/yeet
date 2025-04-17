// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "time"

func main() {
	for {
		println("Hello, World!")
		time.Sleep(2 * time.Second)
	}
}
