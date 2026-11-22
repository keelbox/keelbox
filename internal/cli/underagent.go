package cli

import "os"

// underAgent reports whether keelbox runs under an agent CLI on this
// computer: Claude Code sets CLAUDECODE in its shell, Codex CODEX_THREAD_ID
// or CODEX_SESSION_ID, and KEELBOX_AGENT is for any other. A question keelbox cannot ask without a terminal then
// ends with words for the owner rather than the flag that skips it: the
// flag is the fix for a script, and for an agent it is going around them.
func underAgent() bool {
	for _, v := range []string{"CLAUDECODE", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "KEELBOX_AGENT"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

// orOwner is the end of an error for a question with no terminal to ask
// it on: script, the words with the flags that answer it, or under an
// agent, that the owner runs cmd on their terminal.
func orOwner(script, cmd string) string {
	if underAgent() {
		return "this needs the owner's yes; ask them to run it on their terminal: " + cmd
	}
	return script
}
