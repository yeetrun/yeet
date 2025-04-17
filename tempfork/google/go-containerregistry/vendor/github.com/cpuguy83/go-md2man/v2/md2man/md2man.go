// Copyright 2025 AUTHORS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package md2man

import (
	"github.com/russross/blackfriday/v2"
)

// Render converts a markdown document into a roff formatted document.
func Render(doc []byte) []byte {
	renderer := NewRoffRenderer()

	return blackfriday.Run(doc,
		[]blackfriday.Option{blackfriday.WithRenderer(renderer),
			blackfriday.WithExtensions(renderer.GetExtensions())}...)
}
