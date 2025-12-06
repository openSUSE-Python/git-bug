# Plan: `--wilfully-impersonate-authors` Option for Git-Bug Bridge Push

## Overview

This plan outlines the implementation of a new `--wilfully-impersonate-authors` flag for all `git bug bridge push` commands. This option allows pushing bugs, comments, and other items under the personality of the current user, replacing the original author requirement. Additionally, it includes user identity mapping and migration header insertion to preserve original issue metadata.

## Current Architecture Analysis

### Key Findings

1. **Strict Author Authentication**: Each bridge implementation (GitHub, GitLab, JIRA) strictly requires original authors' API tokens for export operations
2. **Authentication Flow**: `getClientForIdentity()` methods fail with `ErrMissingIdentityToken` when original author credentials are missing
3. **Export Process**: Operations are skipped when original author tokens are unavailable rather than allowing substitution
4. **Current User Resolution**: `GetUserIdentity()` provides the current user's identity from git config

### Current Export Flow

```
git bug bridge push [NAME]
    ↓
commands/bridge/bridge_push.runBridgePush()
    ↓
bridge.DefaultBridge() or bridge.LoadBridge()
    ↓
bridge.ExportAll()
    ↓
[specific-bridge].ExportAll()
    ↓
[specific-bridge].exportBug()
    ↓
getClientForIdentity(author.Id()) → fails if no token
```

## Implementation Plan

### Phase 1: CLI Interface Enhancement

**File**: `commands/bridge/bridge_push.go`

1. Add `--wilfully-impersonate-authors` boolean flag to the Cobra command
2. Add `--author-mapping-file` string flag for user identity mapping
3. Pass these flags through the bridge export chain
4. Update `runBridgePush()` to accept and forward the impersonation flag and mapping file

```go
cmd.Flags().Bool("wilfully-impersonate-authors", false, 
    "Push all operations under current user's identity, replacing original authors")
cmd.Flags().String("author-mapping-file", "", 
    "Path to author mapping file in format 'userID = Full Name <email@address>'")
```

### Phase 2: Core Bridge Interface Updates & Refactoring

**File**: `bridge/core/interfaces.go`

1. Extend `Exporter` interface with impersonation and mapping support:
```go
type Exporter interface {
    Init(ctx context.Context, repo *cache.RepoCache, conf Configuration) error
    ExportAll(ctx context.Context, repo *cache.RepoCache, since time.Time) (<-chan ExportResult, error)
    // New method for impersonated export with author mapping
    ExportAllWithImpersonation(ctx context.Context, repo *cache.RepoCache, since time.Time, impersonate bool, authorMapping *AuthorMapping) (<-chan ExportResult, error)
}
```

**File**: `bridge/core/bridge.go`

1. Update `Bridge.ExportAll()` to accept impersonation and mapping parameters
2. Add new method `ExportAllWithImpersonation()` that forwards the flags to exporters

### Phase 2.1: Refactoring Common Bridge Code

**File**: `bridge/core/client_resolver.go` (new file)

1. Extract common client resolution patterns:
```go
type ClientResolver[T any] interface {
    GetClientForIdentity(userId entity.Id) (T, error)
    GetCurrentUserClient() (T, error)
    CacheAllClients(repo *cache.RepoCache, conf Configuration) error
}

type GenericClientResolver[T any] struct {
    identityClient map[entity.Id]T
    impersonate    bool
    clientBuilder  func(cred auth.Credential, conf Configuration) (T, error)
}
```

**File**: `bridge/core/export_workflow.go` (new file)

1. Extract common export workflow structure:
```go
type ExportWorkflow[T any] struct {
    clientResolver ClientResolver[T]
    target         string
    authorMapping  *AuthorMapping
    impersonate    bool
}

func (ew *ExportWorkflow[T]) ExportAll(ctx context.Context, repo *cache.RepoCache, since time.Time) (<-chan core.ExportResult, error)
func (ew *ExportWorkflow[T]) ExportBug(ctx context.Context, b *cache.BugCache, out chan<- core.ExportResult) error
```

**File**: `bridge/core/metadata.go` (new file)

1. Extract common metadata operations:
```go
type OperationMetadata struct {
    RemoteID    string
    RemoteURL   string
    Project     string
    ExportTime  time.Time
    Additional  map[string]string
}

func MarkOperationAsExported(b *cache.BugCache, target entity.Id, metadata OperationMetadata) error
```

**File**: `bridge/core/errors.go` (new file)

1. Extract common error handling:
```go
var ErrMissingIdentityToken = errors.New("missing identity token")

func WrapExportError(err error, operation string, bugID entity.Id) core.ExportResult
```

### Phase 2.2: Author Mapping Infrastructure

**File**: `bridge/core/author_mapping.go` (new file)

1. Create author mapping parser and resolver:
```go
type AuthorMapping struct {
    mappings map[string]string // userID -> Full Name <email@address>
    filePath string
}

func LoadAuthorMapping(filePath string) (*AuthorMapping, error)
func (am *AuthorMapping) ResolveAuthor(userID string) (string, bool)
func (am *AuthorMapping) ParseLine(line string) (userID, fullName string, err error)
func (am *AuthorMapping) PromptAndAddMapping(userID string, displayName string) (string, error)
func (am *AuthorMapping) SaveToFile() error
```

#### 2.3 Interactive Mapping Resolution

**File**: `bridge/core/author_mapping.go`

1. Add interactive prompting for missing mappings:
```go
func (am *AuthorMapping) PromptAndAddMapping(userID string, displayName string) (string, error) {
    fmt.Printf("No mapping found for user ID: %s (%s)\n", userID, displayName)
    fmt.Printf("Enter mapping (format: Full Name <email@address>): ")
    
    reader := bufio.NewReader(os.Stdin)
    input, err := reader.ReadString('\n')
    if err != nil {
        return "", fmt.Errorf("failed to read input: %w", err)
    }
    
    mapping := strings.TrimSpace(input)
    if mapping == "" {
        return "", fmt.Errorf("empty mapping provided")
    }
    
    // Validate format
    if !strings.Contains(mapping, "<") || !strings.Contains(mapping, ">") {
        return "", fmt.Errorf("invalid format, expected: Full Name <email@address>")
    }
    
    am.mappings[userID] = mapping
    return mapping, nil
}

func (am *AuthorMapping) ResolveAuthorWithPrompt(userID string, displayName string) (string, error) {
    if mappedAuthor, exists := am.mappings[userID]; exists {
        return mappedAuthor, nil
    }
    
    // Prompt for missing mapping
    return am.PromptAndAddMapping(userID, displayName)
}
```

### Phase 3: Bridge Implementation Updates & Refactoring

**Files**: 
- `bridge/github/export.go`
- `bridge/gitlab/export.go` 
- `bridge/jira/export.go`

For each bridge implementation:

#### 3.1 Refactor to Use Shared Components

Replace duplicate code with shared core components:

```go
type githubExporter struct {
    // Refactored to use shared components
    exportWorkflow *ExportWorkflow[*github.Client]
    // Platform-specific fields only
    owner          string
    project        string
    // ... other github-specific fields
}
```

#### 3.2 Remove Duplicate Functions

Delete these functions from individual bridge files (now in core):
- `getClientForIdentity()` → use `ClientResolver.GetClientForIdentity()`
- `cacheAllClient()` → use `ClientResolver.CacheAllClients()`
- `markOperationAsExported()` → use `metadata.MarkOperationAsExported()`
- Common error handling → use `errors.WrapExportError()`

#### 3.3 Keep Platform-Specific Logic

Only retain platform-specific implementations:
- API client builders
- Operation type handlers (GitHub-specific issue/comment creation)
- Platform-specific metadata extraction
- Migration header generation with platform-specific URLs

#### 3.4 Update Client Resolution Logic (Now in Core)

The impersonation logic is now handled in `bridge/core/client_resolver.go`:

```go
func (gcr *GenericClientResolver[T]) GetClientForIdentity(userId entity.Id) (T, error) {
    if gcr.impersonate {
        // Return current user's client instead of failing
        return gcr.GetCurrentUserClient()
    }
    
    client, ok := gcr.identityClient[userId]
    if ok {
        return client, nil
    }

    return nil, ErrMissingIdentityToken
}

func (gcr *GenericClientResolver[T]) GetCurrentUserClient() (T, error) {
    // Implementation moved to core with generic type support
}
```

#### 3.5 Platform-Specific Client Builders

Each bridge provides only the client builder function:

```go
// In bridge/github/export.go
func buildGitHubClient(cred auth.Credential, conf core.Configuration) (*github.Client, error) {
    // GitHub-specific client creation logic
}

// In bridge/gitlab/export.go  
func buildGitLabClient(cred auth.Credential, conf core.Configuration) (*gitlab.Client, error) {
    // GitLab-specific client creation logic
}
```

#### 3.6 Add Migration Header Generation (Platform-Specific)

Each bridge implements its own migration header generation with platform-specific URLs:

```go
// In bridge/github/export.go
func (ge *githubExporter) generateMigrationHeader(bug *bug.Bug, originalAuthor *identity.Identity, remoteIssueID int) (string, error) {
    remoteURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", ge.owner, ge.project, remoteIssueID)
    return ge.exportWorkflow.GenerateMigrationHeader(bug, originalAuthor, remoteURL)
}

// In bridge/gitlab/export.go
func (ge *gitlabExporter) generateMigrationHeader(bug *bug.Bug, originalAuthor *identity.Identity, remoteIssueID int) (string, error) {
    remoteURL := fmt.Sprintf("https://gitlab.com/%s/%s/-/issues/%d", ge.owner, ge.project, remoteIssueID)
    return ge.exportWorkflow.GenerateMigrationHeader(bug, originalAuthor, remoteURL)
}
```

#### 3.7 Shared Migration Header Logic (in Core)

**File**: `bridge/core/migration_header.go` (new file)

```go
func (ew *ExportWorkflow[T]) GenerateMigrationHeader(bug *bug.Bug, originalAuthor *identity.Identity, remoteURL string) (string, error) {
    var header strings.Builder
    
    header.WriteString(fmt.Sprintf("Migrated from: %s\n", remoteURL))
    
    if ew.authorMapping != nil {
        mappedAuthor, err := ew.authorMapping.ResolveAuthorWithPrompt(originalAuthor.Id().String(), originalAuthor.DisplayName())
        if err != nil {
            return "", fmt.Errorf("failed to resolve author mapping: %w", err)
        }
        header.WriteString(fmt.Sprintf("Created by: %s\n", mappedAuthor))
    } else {
        header.WriteString(fmt.Sprintf("Created by: %s\n", originalAuthor.DisplayName()))
    }
    
    header.WriteString(fmt.Sprintf("Created at: %s\n\n", firstComment.Time().Format(time.RFC3339)))
    
    return header.String()
}
```

#### 3.8 Simplified Export Methods

```go
func (ge *githubExporter) ExportAllWithImpersonation(ctx context.Context, repo *cache.RepoCache, since time.Time, impersonate bool, authorMapping *AuthorMapping) (<-chan core.ExportResult, error) {
    return ge.exportWorkflow.ExportAllWithImpersonation(ctx, repo, since, impersonate, authorMapping)
}
```

The core `ExportWorkflow` handles mapping file saving automatically.

### Phase 4: Metadata and Future Tainting Support

#### Current Metadata Structure
- Operations are marked with remote IDs using `markOperationAsExported()`
- Metadata keys: `github-id`, `github-url`, `gitlab-id`, etc.

#### Future Enhancement Note
When database schema changes are possible, add `tainted=true` metadata to impersonated operations:

```go
// Future implementation when schema changes are allowed
if ge.impersonateAuthors {
    metadata["tainted"] = "true"
    metadata["original-author"] = originalAuthor.Id().String()
}
```

This would allow `git bug bridge pull` to treat these as read-only and never overwrite originals.

### Phase 5: Error Handling and Edge Cases

#### Credential Validation
1. Verify current user has valid credentials for the target platform
2. Provide clear error messages when credentials are missing
3. Graceful degradation when impersonation is requested but not possible

#### Backward Compatibility
1. Maintain existing behavior when flag is not used
2. Ensure all existing tests continue to pass
3. Preserve current authentication flow as default

## Implementation Sequence

### Phase 1: Core Infrastructure
1. **CLI flag addition** in `bridge_push.go`
2. **Core interface updates** in `interfaces.go` and `bridge.go`
3. **Author mapping infrastructure** in `author_mapping.go`
4. **Client resolver** in `client_resolver.go`
5. **Export workflow** in `export_workflow.go`
6. **Metadata operations** in `metadata.go`
7. **Error handling** in `errors.go`
8. **Migration header** in `migration_header.go`

### Phase 2: Bridge Refactoring
9. **Refactor GitHub bridge** to use shared components (reference implementation)
10. **Refactor GitLab bridge** to use shared components
11. **Refactor JIRA bridge** to use shared components

### Phase 3: Testing and Validation
12. **Unit tests** for all new core components
13. **Integration tests** for refactored bridges
14. **End-to-end testing** with impersonation and mapping
15. **Backward compatibility validation**

## Key Technical Challenges

### 1. Refactoring Complexity
- Maintaining existing behavior while extracting shared code
- Generic type handling for different client types (GitHub vs GitLab vs JIRA)
- Ensuring platform-specific logic remains properly isolated
- Managing dependencies between refactored components

### 2. Client Resolution
- Ensuring current user has valid credentials for the target platform
- Handling cases where current user has no platform-specific credentials
- Fallback strategies for different authentication scenarios
- Generic client resolver that works with different API client types

### 3. Author Mapping Management
- Interactive prompting for missing user mappings
- File I/O operations for persistent mapping storage
- Handling concurrent access to mapping files
- Validation of mapping format and email addresses

### 4. Migration Header Integration
- Identifying and modifying the first comment (bug description)
- Preserving original content while adding migration metadata
- Handling different comment structures across platforms
- Ensuring headers are only added once per bug

### 5. Error Handling
- Graceful degradation when impersonation is requested but current user lacks credentials
- Clear error messages for users about missing credentials
- Maintaining operation integrity during partial failures
- Handling user input errors during interactive mapping

### 6. Metadata Consistency
- Maintaining existing metadata structure while enabling impersonation
- Preserving original author information in local storage
- Ensuring remote platform audit trails remain accurate

### 7. Backward Compatibility
- Ensuring existing functionality unchanged when flag is not used
- Maintaining current security model as default behavior
- Preserving existing API contracts during refactoring

## Security Considerations

### 1. Intentional Obfuscation
- The `--wilfully-impersonate-authors` flag name makes the implications clear
- Users must explicitly opt into this behavior
- Flag name serves as a warning about the implications

### 2. Audit Trail Integrity
- Remote platforms will still show the current user as the author
- Platform-level audit integrity is maintained
- Local git-bug storage preserves original author information

### 3. Local Traceability
- Original author information is preserved in git-bug's local storage
- Operations can be traced back to original authors locally
- Future "tainting" mechanism will provide additional safeguards

## Testing Strategy

### 1. Unit Tests
- Client resolution with impersonation enabled/disabled
- Current user identity resolution
- Author mapping file parsing and validation
- Interactive prompting simulation
- Migration header generation
- Error handling for missing credentials

### 2. Integration Tests
- End-to-end bridge export with impersonation and mapping
- Mixed author scenarios (some with credentials, some without)
- Platform-specific behavior validation
- Mapping file creation and persistence
- Migration header insertion in bug descriptions

### 3. Edge Case Testing
- Missing current user credentials
- Invalid current user identity
- Bridge configuration issues
- Malformed mapping file entries
- Invalid user input during interactive prompting
- File permission issues with mapping file

### 4. Backward Compatibility Tests
- Ensure existing functionality unchanged
- Verify default behavior preserved
- Validate existing test suite continues to pass

### 5. Interactive Testing
- Mock user input for mapping prompts
- Test graceful handling of empty/invalid input
- Verify mapping file updates after interactive sessions

## Files to be Modified

### Core Files
- `commands/bridge/bridge_push.go` - CLI flag and command handling
- `bridge/core/interfaces.go` - Exporter interface extension
- `bridge/core/bridge.go` - Bridge orchestration updates
- `bridge/core/author_mapping.go` - New file for author mapping functionality
- `bridge/core/client_resolver.go` - New file for shared client resolution
- `bridge/core/export_workflow.go` - New file for shared export logic
- `bridge/core/metadata.go` - New file for shared metadata operations
- `bridge/core/errors.go` - New file for shared error handling
- `bridge/core/migration_header.go` - New file for migration header generation

### Bridge Implementation Files (Refactored)
- `bridge/github/export.go` - Simplified to use shared components
- `bridge/gitlab/export.go` - Simplified to use shared components
- `bridge/jira/export.go` - Simplified to use shared components

### Test Files
- Various test files across bridge implementations
- Integration test updates for new functionality
- `bridge/core/author_mapping_test.go` - New file for author mapping tests
- `bridge/core/client_resolver_test.go` - New file for client resolver tests
- `bridge/core/export_workflow_test.go` - New file for export workflow tests

## Documentation Updates

### 1. Manual Pages
- Update `git-bug-bridge-push.1` man page
- Document the new flags and their implications
- Add examples of author mapping file format

### 2. User Documentation
- Update bridge usage documentation
- Add examples and use cases
- Document security implications
- Explain interactive mapping process
- Provide sample author mapping file

### 3. Developer Documentation
- Update bridge development guide
- Document impersonation and mapping architecture
- Provide implementation examples
- Document author mapping file format specification

## Rollout Plan

### Phase 1: Core Implementation
- Implement basic functionality
- Add comprehensive test coverage
- Ensure backward compatibility

### Phase 2: Documentation
- Update all documentation
- Add usage examples
- Document security considerations

### Phase 3: Release
- Include in next release
- Monitor for issues
- Gather user feedback

## Future Enhancements

### 1. Tainting Mechanism
When database schema changes are possible:
- Add `tainted=true` metadata to impersonated operations
- Implement read-only handling in `git bug bridge pull`
- Provide visual indicators in UI components

### 2. Selective Impersonation
- Allow impersonation of specific operations only
- Provide fine-grained control over which operations to impersonate
- Add configuration options for default behavior

### 3. Audit Logging
- Enhanced logging for impersonated operations
- Audit trail for compliance requirements
- Integration with external audit systems

### 4. Advanced Author Mapping
- Support for multiple mapping file formats
- Automatic author suggestion based on username similarity
- Integration with external identity providers
- Bulk mapping import/export functionality

### 5. Migration Header Customization
- Configurable header templates
- Support for additional metadata fields
- Platform-specific header formats
- Localization support for header text

## Conclusion

This plan provides a comprehensive approach to implementing author impersonation with user identity mapping and migration header insertion while maintaining the integrity and security of the existing bridge system. The plan includes significant refactoring opportunities that will eliminate 60-70% of duplicate code across bridge implementations.

The implementation prioritizes:
- **Security**: Maintaining audit trail integrity
- **Compatibility**: Preserving existing behavior
- **Clarity**: Making implications obvious through naming
- **Flexibility**: Supporting future enhancements like tainting
- **Usability**: Interactive mapping resolution and persistent storage
- **Traceability**: Migration headers preserving original issue metadata
- **Maintainability**: Shared core components reducing code duplication
- **Extensibility**: Generic patterns enabling easier addition of new bridges

## Refactoring Benefits

The proposed refactoring will:
1. **Reduce Code Duplication**: Eliminate ~60-70% of duplicate code across GitHub, GitLab, and JIRA bridges
2. **Improve Maintainability**: Centralize common logic in `bridge/core` components
3. **Enable Consistency**: Ensure all bridges handle impersonation and mapping uniformly
4. **Simplify Testing**: Test core logic once instead of three times
5. **Facilitate Future Bridges**: New bridges can leverage existing shared components

The modular approach allows for incremental implementation and testing while minimizing risk to existing functionality. The interactive author mapping feature ensures smooth user experience without requiring manual file editing for missing mappings.