#!/bin/bash
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

set -e

go version
go test -v google.golang.org/appengine/...
go test -v -race google.golang.org/appengine/...
if [[ $GOAPP == "true" ]]; then
  export PATH="$PATH:/tmp/sdk/go_appengine"
  export APPENGINE_DEV_APPSERVER=/tmp/sdk/go_appengine/dev_appserver.py
  goapp version
  goapp test -v google.golang.org/appengine/...
fi
