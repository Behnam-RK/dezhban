package main

import (
	"flag"
	"fmt"
	"os"
)

// completionUsage documents how to load each shell's script.
const completionUsage = `usage: dezhban completion <bash|zsh|fish>

Load it in your shell:
  bash:  source <(dezhban completion bash)      # or add to ~/.bashrc
  zsh:   source <(dezhban completion zsh)        # or add to ~/.zshrc
  fish:  dezhban completion fish | source        # or > ~/.config/fish/completions/dezhban.fish`

// cmdCompletion prints a shell completion script to stdout. The scripts are
// hand-written (no third-party dependency) and complete subcommands, the
// print-rules --mode values, and fall back to file completion for --config.
func cmdCompletion(args []string) int {
	fs := flag.NewFlagSet("completion", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, completionUsage)
		return 2
	}
	switch fs.Arg(0) {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		fmt.Fprintf(os.Stderr, "unknown shell %q\n\n%s\n", fs.Arg(0), completionUsage)
		return 2
	}
	return 0
}

// completionCommands is the subcommand list the scripts offer. Kept next to the
// scripts so it is obvious to update when a command is added.
const completionCommands = "run block unblock status validate monitor print-rules doctor panic install uninstall start stop restart detect-vpn setup config completion version help"

const bashCompletion = `# dezhban bash completion
_dezhban() {
    local cur prev words cword
    _init_completion 2>/dev/null || {
        cur="${COMP_WORDS[COMP_CWORD]}"
        prev="${COMP_WORDS[COMP_CWORD-1]}"
    }
    case "$prev" in
        --mode) COMPREPLY=( $(compgen -W "guard fullblock switch legacy" -- "$cur") ); return ;;
        --config) COMPREPLY=( $(compgen -f -- "$cur") ); return ;;
        completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ); return ;;
        config) COMPREPLY=( $(compgen -W "path show get set edit" -- "$cur") ); return ;;
    esac
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "` + completionCommands + `" -- "$cur") )
        return
    fi
    case "$cur" in
        -*) COMPREPLY=( $(compgen -W "--config --mode --force --guard --dry-run --once --json --discover --simulate-country --verbose -v" -- "$cur") ) ;;
    esac
}
complete -F _dezhban dezhban
`

const zshCompletion = `#compdef dezhban
# dezhban zsh completion
_dezhban() {
    local -a cmds
    cmds=(` + completionCommands + `)
    if (( CURRENT == 2 )); then
        compadd -- $cmds
        return
    fi
    case "${words[CURRENT-1]}" in
        --mode) compadd -- guard fullblock switch legacy; return ;;
        --config) _files; return ;;
        completion) compadd -- bash zsh fish; return ;;
        config) compadd -- path show get set edit; return ;;
    esac
    compadd -- --config --mode --force --guard --dry-run --once --json --discover --simulate-country --verbose
}
compdef _dezhban dezhban
`

const fishCompletion = `# dezhban fish completion
complete -c dezhban -f
# subcommands (only as the first argument)
complete -c dezhban -n '__fish_use_subcommand' -a '` + completionCommands + `'
# flag values
complete -c dezhban -l mode -x -a 'guard fullblock switch legacy'
complete -c dezhban -l config -r
complete -c dezhban -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c dezhban -n '__fish_seen_subcommand_from config' -a 'path show get set edit'
# common flags
complete -c dezhban -l force
complete -c dezhban -l guard
complete -c dezhban -l dry-run
complete -c dezhban -l once
complete -c dezhban -l json
complete -c dezhban -l discover
complete -c dezhban -l simulate-country -x
complete -c dezhban -s v -l verbose
`
