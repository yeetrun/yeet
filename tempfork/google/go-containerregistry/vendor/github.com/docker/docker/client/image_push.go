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
	"errors"
	"io"
	"net/url"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/errdefs"
)

// ImagePush requests the docker host to push an image to a remote registry.
// It executes the privileged function if the operation is unauthorized
// and it tries one more time.
// It's up to the caller to handle the io.ReadCloser and close it properly.
func (cli *Client) ImagePush(ctx context.Context, image string, options types.ImagePushOptions) (io.ReadCloser, error) {
	ref, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil, err
	}

	if _, isCanonical := ref.(reference.Canonical); isCanonical {
		return nil, errors.New("cannot push a digest reference")
	}

	name := reference.FamiliarName(ref)
	query := url.Values{}
	if !options.All {
		ref = reference.TagNameOnly(ref)
		if tagged, ok := ref.(reference.Tagged); ok {
			query.Set("tag", tagged.Tag())
		}
	}

	resp, err := cli.tryImagePush(ctx, name, query, options.RegistryAuth)
	if errdefs.IsUnauthorized(err) && options.PrivilegeFunc != nil {
		newAuthHeader, privilegeErr := options.PrivilegeFunc()
		if privilegeErr != nil {
			return nil, privilegeErr
		}
		resp, err = cli.tryImagePush(ctx, name, query, newAuthHeader)
	}
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

func (cli *Client) tryImagePush(ctx context.Context, imageID string, query url.Values, registryAuth string) (serverResponse, error) {
	headers := map[string][]string{registry.AuthHeader: {registryAuth}}
	return cli.post(ctx, "/images/"+imageID+"/push", query, nil, headers)
}
