---
description: How to make changes in this repo (branching, worktrees, PRs)
---

# Making Changes in This Repo

Multiple agents or developers may work on this repo concurrently. Always use a **git worktree** to isolate your work.

## 1. Create a worktree and branch

```bash
# From the main repo directory
git worktree add ../cloudstic-cli-<short-description> -b <branch-name> main
```

Pick a descriptive short name, e.g. `cloudstic-cli-unencrypted-e2e` for branch `feat/e2e-unencrypted-backup`.

> [!IMPORTANT]
> Never commit directly to the main worktree at `/Users/loichermann/workspace/cloudstic-cli` — another agent may be using it.

## 2. Make your changes in the new worktree

All file edits and commands should target the worktree path, e.g.:
```
/Users/loichermann/workspace/cloudstic-cli-<short-description>/
```

## 3. Run tests from the worktree

```bash
cd /Users/loichermann/workspace/cloudstic-cli-<short-description>
go test -v ./...
```

For a specific test:
```bash
go test -v -run TestName ./path/to/package/
```

## 4. Commit and push

The `cmd/cloudstic` directory is git-ignored (compiled binary). Use `-f` when staging files under it:
// turbo
```bash
git add -f cmd/cloudstic/main_test.go  # example
git commit -m "category: short description"
git push origin <branch-name>
```

## 5. Create a PR

Use the GitHub MCP tools to create a PR targeting `main`:
- Owner: `Cloudstic`
- Repo: `cli`

## 6. Cleanup (optional)

After the PR is merged, remove the worktree:
```bash
git worktree remove ../cloudstic-cli-<short-description>
```

## Commit message convention

Use conventional-style prefixes:
- `feat:` — new feature
- `fix:` — bug fix
- `test:` — adding or updating tests
- `docs:` — documentation only
- `refactor:` — code change that neither fixes a bug nor adds a feature
