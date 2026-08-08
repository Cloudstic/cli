<!--
PR titles become squash-merge commit subjects. Use a Conventional Commit title:
`type(scope): imperative summary` (for example, `fix(store): reject truncated
pack footers`). Keep it concise, lowercase the summary after the colon, and omit
the trailing period.
-->

## Summary

<!--
Explain what changed and why. Prefer a short set of concrete bullets. Add
measurements, design rationale, screenshots, or scope notes below when they help
a reviewer evaluate this particular change.
-->

-

## Related issues

<!-- Use `Closes #123` when merging this PR should close an issue. -->

Closes #

## Repository compatibility

<!--
If this changes anything written to a store, explain the legacy read path, safe
failure behavior for older builds, and any repository-format version decision.
Read docs/compatibility.md before making such a change. Otherwise keep the line
below.
-->

No repository format change.

## Verification

<!--
List the exact automated and manual checks run, with their result. Include a
focused regression test when fixing a bug. For performance-sensitive changes,
include before/after measurements and consider adding the `benchmark` label.
-->

- `go test -count=1 ./...`
- `golangci-lint run ./...`

## Documentation

<!--
Describe documentation changes, or explain why none are needed. Exported API
changes should be paired with an update to the separate Cloudstic docs repo.
-->

No documentation change required.
