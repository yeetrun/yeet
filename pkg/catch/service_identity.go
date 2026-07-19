// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os/user"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

type resolvedServiceIdentity struct {
	Persisted db.ServiceIdentity
	UserName  string
	GroupName string
}

var (
	serviceUserLookup    = user.Lookup
	serviceUserLookupID  = user.LookupId
	serviceGroupLookup   = user.LookupGroup
	serviceGroupLookupID = user.LookupGroupId
)

func resolveServiceIdentity(spec string) (resolvedServiceIdentity, error) {
	requested, err := parseRequestedServiceIdentity(spec)
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	uid, account, userName, err := resolveServiceUser(requested.user)
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	if account == nil && (!requested.hasGroup || !numericID(requested.group)) {
		return resolvedServiceIdentity{}, fmt.Errorf("numeric UID without an account requires a numeric GID")
	}
	gid, requestedGroup, groupName, err := resolveRequestedServiceIdentityGroup(requested, account)
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	return resolvedServiceIdentity{
		Persisted: db.ServiceIdentity{
			RequestedUser:  requested.user,
			RequestedGroup: requestedGroup,
			UID:            uid,
			GID:            gid,
		},
		UserName:  userName,
		GroupName: groupName,
	}, nil
}

type requestedServiceIdentity struct {
	user     string
	group    string
	hasGroup bool
}

func parseRequestedServiceIdentity(spec string) (requestedServiceIdentity, error) {
	userName, groupName, hasGroup := strings.Cut(strings.TrimSpace(spec), ":")
	if userName == "" {
		return requestedServiceIdentity{}, fmt.Errorf("user must not be empty")
	}
	if hasGroup && groupName == "" {
		return requestedServiceIdentity{}, fmt.Errorf("group must not be empty")
	}
	return requestedServiceIdentity{user: userName, group: groupName, hasGroup: hasGroup}, nil
}

func resolveRequestedServiceIdentityGroup(requested requestedServiceIdentity, account *user.User) (uint32, string, string, error) {
	if requested.hasGroup {
		gid, groupName, err := resolveServiceGroup(requested.group)
		return gid, requested.group, groupName, err
	}
	gid, err := parseID(account.Gid, "GID")
	if err != nil {
		return 0, "", "", fmt.Errorf("user %q has invalid primary GID: %w", requested.user, err)
	}
	group, err := serviceGroupLookupID(account.Gid)
	if err != nil {
		return 0, "", "", fmt.Errorf("primary group %s for user %q does not exist: %w", account.Gid, requested.user, err)
	}
	return gid, group.Name, group.Name, nil
}

func resolveServiceUser(value string) (uint32, *user.User, string, error) {
	if numericID(value) {
		uid, err := parseID(value, "UID")
		if err != nil {
			return 0, nil, "", err
		}
		account, err := serviceUserLookupID(strconv.FormatUint(uint64(uid), 10))
		if err != nil {
			if unknownServiceUser(err) {
				return uid, nil, "", nil
			}
			return 0, nil, "", fmt.Errorf("lookup numeric UID %s: %w", value, err)
		}
		resolvedUID, err := parseID(account.Uid, "UID")
		if err != nil {
			return 0, nil, "", fmt.Errorf("resolved account for UID %s: %w", value, err)
		}
		if resolvedUID != uid {
			return 0, nil, "", fmt.Errorf("numeric UID %s resolved to UID %d", value, resolvedUID)
		}
		return uid, account, account.Username, nil
	}

	account, err := serviceUserLookup(value)
	if err != nil {
		if unknownServiceUser(err) {
			return 0, nil, "", fmt.Errorf("user does not exist: %s", value)
		}
		return 0, nil, "", fmt.Errorf("lookup user %q: %w", value, err)
	}
	uid, err := parseID(account.Uid, "UID")
	if err != nil {
		return 0, nil, "", fmt.Errorf("user %q: %w", value, err)
	}
	return uid, account, account.Username, nil
}

func resolveServiceGroup(value string) (uint32, string, error) {
	if numericID(value) {
		gid, err := parseID(value, "GID")
		if err != nil {
			return 0, "", err
		}
		group, err := serviceGroupLookupID(strconv.FormatUint(uint64(gid), 10))
		if err != nil {
			if unknownServiceGroup(err) {
				return gid, "", nil
			}
			return 0, "", fmt.Errorf("lookup numeric GID %s: %w", value, err)
		}
		resolvedGID, err := parseID(group.Gid, "GID")
		if err != nil {
			return 0, "", fmt.Errorf("resolved group for GID %s: %w", value, err)
		}
		if resolvedGID != gid {
			return 0, "", fmt.Errorf("numeric GID %s resolved to GID %d", value, resolvedGID)
		}
		return gid, group.Name, nil
	}

	group, err := serviceGroupLookup(value)
	if err != nil {
		if unknownServiceGroup(err) {
			return 0, "", fmt.Errorf("group does not exist: %s", value)
		}
		return 0, "", fmt.Errorf("lookup group %q: %w", value, err)
	}
	gid, err := parseID(group.Gid, "GID")
	if err != nil {
		return 0, "", fmt.Errorf("group %q: %w", value, err)
	}
	return gid, group.Name, nil
}

func parseID(value, kind string) (uint32, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", kind, value, err)
	}
	return uint32(n), nil
}

func numericID(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}

func unknownServiceUser(err error) bool {
	var unknownName user.UnknownUserError
	var unknownID user.UnknownUserIdError
	return errors.As(err, &unknownName) || errors.As(err, &unknownID)
}

func unknownServiceGroup(err error) bool {
	var unknownName user.UnknownGroupError
	var unknownID user.UnknownGroupIdError
	return errors.As(err, &unknownName) || errors.As(err, &unknownID)
}

func effectiveServiceIdentity(sv db.ServiceView) resolvedServiceIdentity {
	if !sv.Valid() || !sv.Identity().Valid() {
		return resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "root", RequestedGroup: "root", UID: 0, GID: 0,
		}, UserName: "root", GroupName: "root"}
	}
	id := *sv.Identity().AsStruct()
	return resolvedServiceIdentity{Persisted: id, UserName: id.RequestedUser, GroupName: id.RequestedGroup}
}

func serviceIdentityClass(id *db.ServiceIdentity) string {
	if id == nil {
		return "legacy-root"
	}
	if id.RequestedUser == managedServiceUser && id.RequestedGroup == managedServiceUser {
		return "managed"
	}
	if id.UID == 0 {
		return "explicit-root"
	}
	return "operator"
}

func validateServiceIdentityDrift(id db.ServiceIdentity) error {
	if id.RequestedUser == "root" && id.RequestedGroup == "root" && id.UID == 0 && id.GID == 0 {
		return nil
	}
	resolved, err := resolveServiceIdentity(id.RequestedUser + ":" + id.RequestedGroup)
	if err != nil {
		return fmt.Errorf("resolve persisted service identity: %w", err)
	}
	if resolved.Persisted.UID != id.UID {
		return fmt.Errorf("service identity UID drift: %s now resolves to UID %d, persisted UID is %d", id.RequestedUser, resolved.Persisted.UID, id.UID)
	}
	if resolved.Persisted.GID != id.GID {
		return fmt.Errorf("service identity GID drift: %s now resolves to GID %d, persisted GID is %d", id.RequestedGroup, resolved.Persisted.GID, id.GID)
	}
	return nil
}
