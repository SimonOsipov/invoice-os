# Shell commands

- The Bash tool runs each command in zsh. zsh does not split an unquoted `$var` into words: `kill $pids` on a string of pids kills nothing.
- Keep the pids of background jobs in an array: `pids=()`, then `pids+=($!)` after each `&`.
- Stop the background jobs on every exit. Put the trap first: `trap 'kill $pids 2>/dev/null' EXIT INT TERM HUP`.
- Never use `kill $(jobs -p)` in a trap. A non-interactive zsh lists no jobs there.
- Start no busy loop (`while :; do :; done`) to load the CPU. Reproduce a timing failure with real tests run in parallel.
- A background job that outlives its Claude Code session is a stray. The Harbourmaster daemon stops a stray after 30 minutes.
