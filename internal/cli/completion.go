package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// keelbox completion SHELL prints a completion script generated from the
// command table in help.go, so it always offers the commands and flags this
// binary has. Box names come from `keelbox __boxes`, which lists the
// registry and is not shown in help.

func cmdCompletion(pos []string, stdout, stderr io.Writer) int {
	if len(pos) != 1 {
		failf(stderr, "completion needs a shell: keelbox completion zsh (or bash, fish)")
		return 2
	}
	switch pos[0] {
	case "bash":
		fmt.Fprint(stdout, bashCompletion())
	case "zsh":
		fmt.Fprint(stdout, zshCompletion())
	case "fish":
		fmt.Fprint(stdout, fishCompletion())
	default:
		failf(stderr, "completion for %q: use bash, zsh or fish, for example: keelbox completion zsh\n", pos[0])
		return 2
	}
	return 0
}

func cmdBoxes(stdout io.Writer) int {
	for _, b := range append(registeredBoxes(), pendingBoxes()...) {
		fmt.Fprintln(stdout, b)
	}
	return 0
}

// flagNames are a command's flags as typed, without their arguments.
func flagNames(c cmdDoc) []string {
	var out []string
	for _, f := range c.flags {
		name, _, _ := strings.Cut(f.flag, " ")
		name, _, _ = strings.Cut(name, "=")
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// takesBox reports whether a command's argument is a box.
func takesBox(c cmdDoc) bool { return strings.Contains(c.args, "[box]") }

func commandNames() []string {
	var out []string
	for _, c := range commands {
		out = append(out, c.name)
	}
	return out
}

func bashCompletion() string {
	var cases strings.Builder
	for _, c := range commands {
		box := ""
		if takesBox(c) {
			box = " $(keelbox __boxes 2>/dev/null)"
		}
		if c.name == "completion" {
			box = " bash zsh fish"
		}
		fmt.Fprintf(&cases, "    %s) words=%q ;;\n", c.name, strings.Join(flagNames(c), " ")+box)
	}
	return `# keelbox completion for bash. Install:
#   keelbox completion bash > ~/.local/share/bash-completion/completions/keelbox
_keelbox() {
  local cur=${COMP_WORDS[COMP_CWORD]} words
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=($(compgen -W "` + strings.Join(commandNames(), " ") + ` help" -- "$cur"))
    return
  fi
  case ${COMP_WORDS[1]} in
` + cases.String() + `    help) words="` + strings.Join(commandNames(), " ") + `" ;;
    *) return ;;
  esac
  case ${COMP_WORDS[COMP_CWORD-1]} in
    --ssh-key|--binary|--token-file|--target) COMPREPLY=($(compgen -f -- "$cur")); return ;;
    --provider) COMPREPLY=($(compgen -W "ssh local hetzner aws digitalocean gcp cloud-init" -- "$cur")); return ;;
    --agents) COMPREPLY=($(compgen -W "claude-code codex claude-code,codex none" -- "$cur")); return ;;
  esac
  COMPREPLY=($(compgen -W "$words" -- "$cur"))
}
complete -F _keelbox keelbox
`
}

func zshCompletion() string {
	var cmds, cases strings.Builder
	for _, c := range commands {
		fmt.Fprintf(&cmds, "    %s:%s\n", zshQuote(c.name), zshQuote(c.summary))
	}
	for _, c := range commands {
		var specs []string
		for _, f := range c.flags {
			name, arg, _ := strings.Cut(f.flag, " ")
			name, _, _ = strings.Cut(name, "=")
			help := strings.ReplaceAll(strings.ReplaceAll(f.help, "[", `\[`), "]", `\]`)
			help = strings.ReplaceAll(help, ":", `\:`)
			spec := fmt.Sprintf("'%s[%s]", name, strings.ReplaceAll(help, "'", `'\''`))
			switch {
			case name == "--provider":
				spec += ":where:(ssh local hetzner aws digitalocean gcp cloud-init)"
			case name == "--agents":
				spec += ":agents:(claude-code codex claude-code,codex none)"
			case arg == "PATH":
				spec += ":path:_files"
			case arg != "":
				spec += ":" + strings.ToLower(arg) + ":"
			}
			specs = append(specs, spec+"'")
		}
		switch {
		case takesBox(c):
			specs = append(specs, "'*:box:_keelbox_boxes'")
		case c.name == "completion":
			specs = append(specs, "'1:shell:(bash zsh fish)'")
		}
		if len(specs) == 0 {
			continue
		}
		fmt.Fprintf(&cases, "    (%s) _arguments \\\n      %s ;;\n", c.name, strings.Join(specs, " \\\n      "))
	}
	return `#compdef keelbox
# keelbox completion for zsh. Install, in a directory on $fpath:
#   keelbox completion zsh > "${fpath[1]}/_keelbox"
# or load it in ~/.zshrc after compinit: source <(keelbox completion zsh)

_keelbox_boxes() {
  local -a boxes
  boxes=(${(f)"$(keelbox __boxes 2>/dev/null)"})
  _describe 'box' boxes
}

_keelbox() {
  local -a cmds
  cmds=(
` + cmds.String() + `  )
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
    return
  fi
  local cmd=${words[2]}
  shift words
  (( CURRENT-- ))
  case $cmd in
` + cases.String() + `    (help) _describe 'command' cmds ;;
  esac
}

if [ "$funcstack[1]" = "_keelbox" ]; then
  _keelbox "$@"
else
  compdef _keelbox keelbox
fi
`
}

func zshQuote(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, ":", `\:`), "'", `'\''`) + "'"
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString(`# keelbox completion for fish. Install:
#   keelbox completion fish > ~/.config/fish/completions/keelbox.fish
complete -c keelbox -f
`)
	all := strings.Join(append(commandNames(), "help"), " ")
	for _, c := range commands {
		fmt.Fprintf(&b, "complete -c keelbox -n 'not __fish_seen_subcommand_from %s' -a %s -d %s\n", all, c.name, fishQuote(c.summary))
	}
	for _, c := range commands {
		cond := "__fish_seen_subcommand_from " + c.name
		for _, f := range c.flags {
			name, arg, _ := strings.Cut(f.flag, " ")
			name, _, _ = strings.Cut(name, "=")
			line := fmt.Sprintf("complete -c keelbox -n '%s' -l %s -d %s", cond, strings.TrimPrefix(name, "--"), fishQuote(f.help))
			switch {
			case name == "--provider":
				line += " -x -a 'ssh local hetzner aws digitalocean gcp cloud-init'"
			case name == "--agents":
				line += " -x -a 'claude-code codex claude-code,codex none'"
			case arg == "PATH":
				line += " -r -F"
			case arg != "":
				line += " -x"
			}
			b.WriteString(line + "\n")
		}
		if takesBox(c) {
			fmt.Fprintf(&b, "complete -c keelbox -n '%s' -a '(keelbox __boxes 2>/dev/null)'\n", cond)
		}
		if c.name == "completion" {
			fmt.Fprintf(&b, "complete -c keelbox -n '%s' -a 'bash zsh fish'\n", cond)
		}
	}
	return b.String()
}

func fishQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `\'`) + "'" }
