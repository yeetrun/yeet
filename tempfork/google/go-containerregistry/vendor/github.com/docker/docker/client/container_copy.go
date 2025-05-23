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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
)

// ContainerStatPath returns stat information about a path inside the container filesystem.
func (cli *Client) ContainerStatPath(ctx context.Context, containerID, path string) (types.ContainerPathStat, error) {
	query := url.Values{}
	query.Set("path", filepath.ToSlash(path)) // Normalize the paths used in the API.

	urlStr := "/containers/" + containerID + "/archive"
	response, err := cli.head(ctx, urlStr, query, nil)
	defer ensureReaderClosed(response)
	if err != nil {
		return types.ContainerPathStat{}, err
	}
	return getContainerPathStatFromHeader(response.header)
}

// CopyToContainer copies content into the container filesystem.
// Note that `content` must be a Reader for a TAR archive
func (cli *Client) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options types.CopyToContainerOptions) error {
	query := url.Values{}
	query.Set("path", filepath.ToSlash(dstPath)) // Normalize the paths used in the API.
	// Do not allow for an existing directory to be overwritten by a non-directory and vice versa.
	if !options.AllowOverwriteDirWithFile {
		query.Set("noOverwriteDirNonDir", "true")
	}

	if options.CopyUIDGID {
		query.Set("copyUIDGID", "true")
	}

	apiPath := "/containers/" + containerID + "/archive"

	response, err := cli.putRaw(ctx, apiPath, query, content, nil)
	defer ensureReaderClosed(response)
	if err != nil {
		return err
	}

	return nil
}

// CopyFromContainer gets the content from the container and returns it as a Reader
// for a TAR archive to manipulate it in the host. It's up to the caller to close the reader.
func (cli *Client) CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, types.ContainerPathStat, error) {
	query := make(url.Values, 1)
	query.Set("path", filepath.ToSlash(srcPath)) // Normalize the paths used in the API.

	apiPath := "/containers/" + containerID + "/archive"
	response, err := cli.get(ctx, apiPath, query, nil)
	if err != nil {
		return nil, types.ContainerPathStat{}, err
	}

	// In order to get the copy behavior right, we need to know information
	// about both the source and the destination. The response headers include
	// stat info about the source that we can use in deciding exactly how to
	// copy it locally. Along with the stat info about the local destination,
	// we have everything we need to handle the multiple possibilities there
	// can be when copying a file/dir from one location to another file/dir.
	stat, err := getContainerPathStatFromHeader(response.header)
	if err != nil {
		return nil, stat, fmt.Errorf("unable to get resource stat from response: %s", err)
	}
	return response.body, stat, err
}

func getContainerPathStatFromHeader(header http.Header) (types.ContainerPathStat, error) {
	var stat types.ContainerPathStat

	encodedStat := header.Get("X-Docker-Container-Path-Stat")
	statDecoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encodedStat))

	err := json.NewDecoder(statDecoder).Decode(&stat)
	if err != nil {
		err = fmt.Errorf("unable to decode container path stat header: %s", err)
	}

	return stat, err
}
