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

package client // import "github.com/docker/docker/client"

import "context"

// SecretRemove removes a secret.
func (cli *Client) SecretRemove(ctx context.Context, id string) error {
	if err := cli.NewVersionError("1.25", "secret remove"); err != nil {
		return err
	}
	resp, err := cli.delete(ctx, "/secrets/"+id, nil, nil)
	defer ensureReaderClosed(resp)
	return err
}
