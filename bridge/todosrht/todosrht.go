// Package todosrht contains the TodoSourceHut bridge implementation
package todosrht

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/git-bug/git-bug/bridge/core"
	"github.com/git-bug/git-bug/bridge/core/auth"
	"github.com/git-bug/git-bug/commands/input"
)

const (
	target = "todosrht"

	metaKeyTodoSourceHutId         = "todosrht-id"
	metaKeyTodoSourceHutDerivedId  = "todosrht-derived-id"
	metaKeyTodoSourceHutKey        = "todosrht-key"
	metaKeyTodoSourceHutUser       = "todosrht-user"
	metaKeyTodoSourceHutProject    = "todosrht-project"
	metaKeyTodoSourceHutBaseUrl    = "todosrht-base-url"
	metaKeyTodoSourceHutExportTime = "todosrht-export-time"
	metaKeyTodoSourceHutLogin      = "todosrht-login"

	confKeyBaseUrl        = "base-url"
	confKeyProject        = "project"
	confKeyDefaultLogin   = "default-login"
	confKeyCredentialType = "credentials-type" // "SESSION" or "TOKEN"
	confKeyIDMap          = "bug-id-map"
	confKeyIDRevMap       = "bug-id-revmap"
	// the issue type when exporting a new bug. Default is Story (10001)
	confKeyCreateDefaults = "create-issue-defaults"
	// if set, the bridge fill this TODOSRHT field with the `git-bug` id when exporting
	confKeyCreateGitBug = "create-issue-gitbug-id"

	defaultTimeout = 60 * time.Second
)

var _ core.BridgeImpl = &TodoSourceHut{}

// TodoSourceHut Main object for the bridge
type TodoSourceHut struct{}

// Target returns "todosrht"
func (*TodoSourceHut) Target() string {
	return target
}

func (*TodoSourceHut) LoginMetaKey() string {
	return metaKeyTodoSourceHutLogin
}

// NewImporter returns the todosrht importer
func (*TodoSourceHut) NewImporter() core.Importer {
	return &todosrhtImporter{}
}

// NewExporter returns the todosrht exporter
func (*TodoSourceHut) NewExporter() core.Exporter {
	return &todosrhtExporter{}
}

func buildClient(ctx context.Context, baseURL string, credType string, cred auth.Credential) (*Client, error) {
	client := NewClient(ctx, baseURL)

	var login, password string

	switch cred := cred.(type) {
	case *auth.LoginPassword:
		login = cred.Login
		password = cred.Password
	case *auth.Login:
		login = cred.Login
		p, err := input.PromptPassword(fmt.Sprintf("Password for %s", login), "password", input.Required)
		if err != nil {
			return nil, err
		}
		password = p
	}

	err := client.Login(credType, login, password)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// stringInSlice returns true if needle is found in haystack
func stringInSlice(needle string, haystack []string) bool {
	for _, match := range haystack {
		if match == needle {
			return true
		}
	}
	return false
}

// Given two string slices, return three lists containing:
// 1. elements found only in the first input list
// 2. elements found only in the second input list
// 3. elements found in both input lists
func setSymmetricDifference(setA, setB []string) ([]string, []string, []string) {
	sort.Strings(setA)
	sort.Strings(setB)

	maxLen := len(setA) + len(setB)
	onlyA := make([]string, 0, maxLen)
	onlyB := make([]string, 0, maxLen)
	both := make([]string, 0, maxLen)

	idxA := 0
	idxB := 0

	for idxA < len(setA) && idxB < len(setB) {
		if setA[idxA] < setB[idxB] {
			// In the first set, but not the second
			onlyA = append(onlyA, setA[idxA])
			idxA++
		} else if setA[idxA] > setB[idxB] {
			// In the second set, but not the first
			onlyB = append(onlyB, setB[idxB])
			idxB++
		} else {
			// In both
			both = append(both, setA[idxA])
			idxA++
			idxB++
		}
	}

	for ; idxA < len(setA); idxA++ {
		// Leftovers in the first set, not the second
		onlyA = append(onlyA, setA[idxA])
	}

	for ; idxB < len(setB); idxB++ {
		// Leftovers in the second set, not the first
		onlyB = append(onlyB, setB[idxB])
	}

	return onlyA, onlyB, both
}
