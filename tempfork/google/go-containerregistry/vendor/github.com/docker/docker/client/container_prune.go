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
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
)

// ContainersPrune requests the daemon to delete unused data
func (cli *Client) ContainersPrune(ctx context.Context, pruneFilters filters.Args) (types.ContainersPruneReport, error) {
	var report types.ContainersPruneReport

	if err := cli.NewVersionError("1.25", "container prune"); err != nil {
		return report, err
	}

	query, err := getFiltersQuery(pruneFilters)
	if err != nil {
		return report, err
	}

	serverResp, err := cli.post(ctx, "/containers/prune", query, nil, nil)
	defer ensureReaderClosed(serverResp)
	if err != nil {
		return report, err
	}

	if err := json.NewDecoder(serverResp.body).Decode(&report); err != nil {
		return report, fmt.Errorf("Error retrieving disk usage: %v", err)
	}

	return report, nil
}
