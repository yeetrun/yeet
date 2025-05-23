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

package fs

// https://learn.microsoft.com/en-us/windows/win32/api/winnt/ne-winnt-security_impersonation_level
type SecurityImpersonationLevel int32 // C default enums underlying type is `int`, which is Go `int32`

// Impersonation levels
const (
	SecurityAnonymous      SecurityImpersonationLevel = 0
	SecurityIdentification SecurityImpersonationLevel = 1
	SecurityImpersonation  SecurityImpersonationLevel = 2
	SecurityDelegation     SecurityImpersonationLevel = 3
)
