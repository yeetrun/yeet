// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Error codes defined by OCI Distribution Specification
const (
	// ErrCodeBlobUnknown indicates blob is unknown to the registry
	ErrCodeBlobUnknown = "BLOB_UNKNOWN"
	// ErrCodeBlobUploadInvalid indicates blob upload is invalid
	ErrCodeBlobUploadInvalid = "BLOB_UPLOAD_INVALID"
	// ErrCodeBlobUploadUnknown indicates blob upload session is unknown
	ErrCodeBlobUploadUnknown = "BLOB_UPLOAD_UNKNOWN"
	// ErrCodeDigestInvalid indicates provided digest did not match uploaded content
	ErrCodeDigestInvalid = "DIGEST_INVALID"
	// ErrCodeManifestBlobUnknown indicates blob unknown to registry
	ErrCodeManifestBlobUnknown = "MANIFEST_BLOB_UNKNOWN"
	// ErrCodeManifestInvalid indicates manifest is invalid
	ErrCodeManifestInvalid = "MANIFEST_INVALID"
	// ErrCodeManifestUnknown indicates manifest is unknown
	ErrCodeManifestUnknown = "MANIFEST_UNKNOWN"
	// ErrCodeNameInvalid indicates invalid repository name
	ErrCodeNameInvalid = "NAME_INVALID"
	// ErrCodeNameUnknown indicates repository name not known
	ErrCodeNameUnknown = "NAME_UNKNOWN"
	// ErrCodeSizeInvalid indicates provided length did not match content length
	ErrCodeSizeInvalid = "SIZE_INVALID"
	// ErrCodeUnauthorized indicates authentication required
	ErrCodeUnauthorized = "UNAUTHORIZED"
	// ErrCodeDenied indicates requested access denied
	ErrCodeDenied = "DENIED"
	// ErrCodeUnsupported indicates operation is unsupported
	ErrCodeUnsupported = "UNSUPPORTED"
	// ErrCodeTooManyRequests indicates too many requests
	ErrCodeTooManyRequests = "TOOMANYREQUESTS"
)

// ErrorDescriptor represents an OCI registry error.
type ErrorDescriptor struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

// ErrorResponse represents the OCI-compliant error response format.
type ErrorResponse struct {
	Errors []ErrorDescriptor `json:"errors"`
}

// WriteError writes an OCI-compliant error response.
func WriteError(w http.ResponseWriter, statusCode int, code, message string, detail any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	resp := ErrorResponse{
		Errors: []ErrorDescriptor{
			{
				Code:    code,
				Message: message,
				Detail:  detail,
			},
		},
	}

	json.NewEncoder(w).Encode(resp)
}

// NewError creates a new error descriptor.
func NewError(code, message string) ErrorDescriptor {
	return ErrorDescriptor{
		Code:    code,
		Message: message,
	}
}

// Error implements the error interface.
func (e ErrorDescriptor) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
