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

import (
	"context"
	"net/url"
)

// ContainerRename changes the name of a given container.
func (cli *Client) ContainerRename(ctx context.Context, containerID, newContainerName string) error {
	query := url.Values{}
	query.Set("name", newContainerName)
	resp, err := cli.post(ctx, "/containers/"+containerID+"/rename", query, nil, nil)
	ensureReaderClosed(resp)
	return err
}
