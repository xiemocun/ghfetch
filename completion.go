package main

import (
	"errors"
	"fmt"
)

// The completion scripts are generated from the same flag list the usage text
// documents. TestCompletionScriptsCoverUsage fails when a documented flag is
// missing from a script, so the completions cannot drift behind the CLI.
const (
	completionCommands = "install list completion version help"

	bashCompletion = `# bash completion for ghfetch
# Install: ghfetch completion bash > /etc/bash_completion.d/ghfetch
_ghfetch_commands='install list completion version help'
_ghfetch_install_flags='--to --asset --bin --dest --source --require-checksum --attestation --verify-attestation --require-attestation --dry-run --json'
_ghfetch_list_flags='--json'
_ghfetch_shells='bash zsh fish'
_ghfetch_attestation='off optional required'

_ghfetch() {
    local cur prev cmd i
    COMPREPLY=()
    cur=${COMP_WORDS[COMP_CWORD]}
    prev=${COMP_WORDS[COMP_CWORD-1]}

    case $prev in
        --dest)
            COMPREPLY=( $(compgen -d -- "$cur") )
            return 0
            ;;
        --attestation)
            COMPREPLY=( $(compgen -W "$_ghfetch_attestation" -- "$cur") )
            return 0
            ;;
        --to|--source)
            COMPREPLY=( $(compgen -f -- "$cur") )
            return 0
            ;;
    esac

    cmd=''
    for (( i = 1; i < COMP_CWORD; i++ )); do
        case ${COMP_WORDS[i]} in
            -*) ;;
            *) cmd=${COMP_WORDS[i]}; break ;;
        esac
    done

    if [[ -z $cmd ]]; then
        COMPREPLY=( $(compgen -W "$_ghfetch_commands" -- "$cur") )
        return 0
    fi

    case $cmd in
        completion)
            COMPREPLY=( $(compgen -W "$_ghfetch_shells" -- "$cur") )
            ;;
        install)
            COMPREPLY=( $(compgen -W "$_ghfetch_install_flags" -- "$cur") )
            ;;
        list)
            COMPREPLY=( $(compgen -W "$_ghfetch_list_flags" -- "$cur") )
            ;;
    esac
    return 0
}

complete -F _ghfetch ghfetch
`

	zshCompletion = `#compdef ghfetch
# zsh completion for ghfetch
# Install: ghfetch completion zsh > "${fpath[1]}/_ghfetch"

_ghfetch() {
  local -a commands
  commands=(
    'install:install a release binary'
    'list:list the release assets for a repository'
    'completion:print a shell completion script'
    'version:print the version, Go version and host triplet'
    'help:print usage'
  )

  _arguments -C \
    '(-h --help)'{-h,--help}'[print usage]' \
    '(-v --version)'{-v,--version}'[print the version]' \
    '1:command:->command' \
    '*::argument:->argument'

  case $state in
    command)
      _describe -t commands 'ghfetch command' commands
      ;;
    argument)
      case $words[1] in
        completion)
          _values 'shell' bash zsh fish
          ;;
        install)
          _arguments \
            '--to[use this exact release asset]:file:_files' \
            '--asset[choose the asset by regular expression]:regexp:' \
            '--bin[executable inside the archive]:name:' \
            '--dest[install directory]:directory:_files -/' \
            '--source[comma separated mirror prefixes]:sources:' \
            '--require-checksum[fail when the release publishes no checksum file]' \
            '--attestation[SLSA provenance policy]:policy:(off optional required)' \
            '--verify-attestation[alias for --attestation=optional]' \
            '--require-attestation[alias for --attestation=required]' \
            '--dry-run[resolve and print the plan, install nothing]' \
            '--json[emit a machine readable report on stdout]' \
            '1:repository:'
          ;;
        list)
          _arguments \
            '--json[emit JSON on stdout]' \
            '1:repository:'
          ;;
      esac
      ;;
  esac
}

_ghfetch "$@"
`

	fishCompletion = `# fish completion for ghfetch
# Install: ghfetch completion fish > ~/.config/fish/completions/ghfetch.fish

complete -c ghfetch -f

complete -c ghfetch -n '__fish_use_subcommand' -a install -d 'install a release binary'
complete -c ghfetch -n '__fish_use_subcommand' -a list -d 'list the release assets for a repository'
complete -c ghfetch -n '__fish_use_subcommand' -a completion -d 'print a shell completion script'
complete -c ghfetch -n '__fish_use_subcommand' -a version -d 'print the version'
complete -c ghfetch -n '__fish_use_subcommand' -a help -d 'print usage'

complete -c ghfetch -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'

complete -c ghfetch -n '__fish_seen_subcommand_from install' -l to -r -F -d 'exact release asset'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l asset -r -d 'asset regular expression'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l bin -r -d 'executable inside the archive'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l dest -r -a '(__fish_complete_directories)' -d 'install directory'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l source -r -d 'comma separated mirror prefixes'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l require-checksum -d 'fail when no checksum file is published'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l attestation -r -a 'off optional required' -d 'SLSA provenance policy'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l verify-attestation -d 'alias for --attestation=optional'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l require-attestation -d 'alias for --attestation=required'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l dry-run -d 'resolve only, install nothing'
complete -c ghfetch -n '__fish_seen_subcommand_from install' -l json -d 'machine readable report'

complete -c ghfetch -n '__fish_seen_subcommand_from list' -l json -d 'emit JSON on stdout'
`
)

// cmdCompletion prints a completion script on stdout so it can be sourced
// directly or redirected into the shell's completion directory.
func cmdCompletion(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: ghfetch completion <bash|zsh|fish>")
	}
	var script string
	switch args[0] {
	case "bash":
		script = bashCompletion
	case "zsh":
		script = zshCompletion
	case "fish":
		script = fishCompletion
	default:
		return fmt.Errorf("unknown shell %q, want bash, zsh or fish", args[0])
	}
	_, err := fmt.Print(script)
	return err
}
