#!/bin/bash -e
# Copyright 2025 AUTHORS
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

#
# This script rebuilds the generated code for the protocol buffers.
# To run this you will need protoc and goprotobuf installed;
# see https://github.com/golang/protobuf for instructions.

PKG=google.golang.org/appengine

function die() {
	echo 1>&2 $*
	exit 1
}

# Sanity check that the right tools are accessible.
for tool in go protoc protoc-gen-go; do
	q=$(which $tool) || die "didn't find $tool"
	echo 1>&2 "$tool: $q"
done

echo -n 1>&2 "finding package dir... "
pkgdir=$(go list -f '{{.Dir}}' $PKG)
echo 1>&2 $pkgdir
base=$(echo $pkgdir | sed "s,/$PKG\$,,")
echo 1>&2 "base: $base"
cd $base

# Run protoc once per package.
for dir in $(find $PKG/internal -name '*.proto' | xargs dirname | sort | uniq); do
	echo 1>&2 "* $dir"
	protoc --go_out=. $dir/*.proto
done

for f in $(find $PKG/internal -name '*.pb.go'); do
  # Remove proto.RegisterEnum calls.
  # These cause duplicate registration panics when these packages
  # are used on classic App Engine. proto.RegisterEnum only affects
  # parsing the text format; we don't care about that.
  # https://code.google.com/p/googleappengine/issues/detail?id=11670#c17
  sed -i '/proto.RegisterEnum/d' $f
done
