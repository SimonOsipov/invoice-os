// Package repotest holds what the tests that walk the repository's source tree share; only tests import it.
package repotest

// SkipDirs names the directories a walk of the repo root prunes: git data, sibling
// worktrees (.claude), Ralph's gitignored scratch (.ralph) and installed packages.
var SkipDirs = map[string]bool{".git": true, ".claude": true, ".ralph": true, "node_modules": true}
