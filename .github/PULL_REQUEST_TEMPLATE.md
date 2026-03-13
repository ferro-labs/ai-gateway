## Summary

<!-- One or two sentences describing what this PR does and why. -->

## Type of change

<!-- Check all that apply. -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] New provider
- [ ] New plugin
- [ ] Breaking change (fix or feature that would cause existing functionality to change)
- [ ] Performance improvement
- [ ] Refactor / code quality
- [ ] Documentation / comments only
- [ ] CI / tooling

## Related issues

<!-- Link any related issues: "Fixes #123", "Closes #456", "Part of #789" -->

## Changes

<!-- Bullet-point list of the notable changes in this PR. -->

-
-

## Testing

<!-- Describe how you tested this change. -->

- [ ] Existing tests pass (`go test ./...`)
- [ ] New unit tests added (if applicable)
- [ ] Manually tested against a live provider (if provider change)

## Provider checklist (fill in only for new/updated providers)

- [ ] Provider file at `providers/<id>/<id>.go`
- [ ] Test file at `providers/<id>/<id>_test.go`
- [ ] `ProviderEntry` added to `providers/providers_list.go`
- [ ] Name constant added to `providers/names.go`
- [ ] Models added to `models/catalog.json`
- [ ] `AllowedTools`, `MaxCallDepth` and streaming tested

## Plugin checklist (fill in only for new/updated plugins)

- [ ] Plugin file at `internal/plugins/<name>/`
- [ ] `Stage` (before/after request) correctly set
- [ ] Test coverage added
- [ ] Example config documented

## Breaking change notes

<!-- If this is a breaking change, describe what callers need to update. -->

## Screenshots / output (optional)

<!-- Paste relevant terminal output, benchmark results, or screenshots. -->
