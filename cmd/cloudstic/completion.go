package main

import (
	"fmt"
	"io"
	"os"
)

func runCompletion() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: cloudstic completion <shell>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Available shells: bash, zsh, fish")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Setup:")
		fmt.Fprintln(os.Stderr, "  bash:  source <(cloudstic completion bash)")
		fmt.Fprintln(os.Stderr, "  zsh:   source <(cloudstic completion zsh)")
		fmt.Fprintln(os.Stderr, "  fish:  cloudstic completion fish | source")
		os.Exit(1)
	}

	shell := os.Args[2]
	switch shell {
	case "bash":
		completionBash(os.Stdout)
	case "zsh":
		completionZsh(os.Stdout)
	case "fish":
		completionFish(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "Unsupported shell: %s\nAvailable shells: bash, zsh, fish\n", shell)
		os.Exit(1)
	}
}

// completionBash writes a bash completion script to w.
func completionBash(w io.Writer) {
	_, _ = fmt.Fprint(w, `# bash completion for cloudstic

_cloudstic() {
    local cur prev words cword
    _init_completion || return

    local commands="init backup restore list ls prune forget diff break-lock key cat completion version help"

    local global_flags="-store -s3-endpoint -s3-region -s3-access-key -s3-secret-key -source-sftp-password -source-sftp-key -store-sftp-password -store-sftp-key -encryption-key -password -recovery-key -kms-key-arn -kms-region -kms-endpoint -disable-packfile -prompt -verbose -quiet -debug"

    # Identify the subcommand
    local cmd=""
    local i
    for ((i=1; i < cword; i++)); do
        case "${words[i]}" in
            -*)
                # skip flags and their values
                case "${words[i]}" in
                    -store|-s3-endpoint|-s3-region|-s3-access-key|-s3-secret-key|-source-sftp-password|-source-sftp-key|-store-sftp-password|-store-sftp-key|-encryption-key|-password|-recovery-key|-kms-key-arn|-kms-region|-kms-endpoint|-source|-drive-id|-google-credentials|-google-token-file|-onedrive-client-id|-onedrive-token-file|-tag|-output|-keep-last|-keep-hourly|-keep-daily|-keep-weekly|-keep-monthly|-keep-yearly|-group-by|-account|-json)
                        ((i++)) ;;
                esac
                ;;
            *)
                cmd="${words[i]}"
                break
                ;;
        esac
    done

    # Complete subcommand
    if [[ -z "$cmd" ]]; then
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi

    # Complete flags per subcommand
    local cmd_flags=""
    case "$cmd" in
        init)
            cmd_flags="-add-recovery-key -no-encryption -adopt-slots" ;;
        backup)
            cmd_flags="-source -drive-id -skip-native-files -google-credentials -google-token-file -onedrive-client-id -onedrive-token-file -tag -dry-run" ;;
        restore)
            cmd_flags="-output -dry-run" ;;
        prune)
            cmd_flags="-dry-run" ;;
        forget)
            cmd_flags="-prune -dry-run -keep-last -keep-hourly -keep-daily -keep-weekly -keep-monthly -keep-yearly -tag -source -account -group-by" ;;
        cat)
            cmd_flags="-json -raw" ;;
        completion)
            COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur"))
            return ;;
        key)
            # Handle key subcommands
            local key_sub=""
            local j
            for ((j=i+1; j < cword; j++)); do
                case "${words[j]}" in
                    -*) ;;
                    *) key_sub="${words[j]}"; break ;;
                esac
            done
            if [[ -z "$key_sub" ]]; then
                COMPREPLY=($(compgen -W "list add-recovery passwd" -- "$cur"))
                return
            fi
            case "$key_sub" in
                passwd)
                    cmd_flags="-new-password" ;;
                *)
                    cmd_flags="" ;;
            esac
            ;;
        list)
            cmd_flags="-group" ;;
        check)
            cmd_flags="-read-data" ;;
        ls|diff|break-lock|version|help)
            cmd_flags="" ;;
    esac

    if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "$cmd_flags $global_flags" -- "$cur"))
        return
    fi

    # Value completions for specific flags
    case "$prev" in
        -store)
            # URI completion hint: show scheme prefixes
            COMPREPLY=($(compgen -W "local: s3: b2: sftp://" -- "$cur"))
            return ;;
        -source)
            # URI completion hint: show scheme prefixes and bare keywords
            COMPREPLY=($(compgen -W "local: sftp:// gdrive gdrive-changes onedrive onedrive-changes" -- "$cur"))
            return ;;
        -source-sftp-key|-store-sftp-key|-output)
            _filedir
            return ;;
    esac
}

complete -F _cloudstic cloudstic
`)
}

// completionZsh writes a zsh completion script to w.
func completionZsh(w io.Writer) {
	_, _ = fmt.Fprint(w, `#compdef cloudstic

# zsh completion for cloudstic

_cloudstic() {
    local -a commands
    commands=(
        'init:Initialize a new repository'
        'backup:Create a new backup snapshot from a source'
        'restore:Restore files from a backup snapshot'
        'list:List all backup snapshots in the repository'
        'ls:List files within a specific snapshot'
        'prune:Remove unused data chunks from the repository'
        'forget:Remove a specific snapshot from history'
        'diff:Compare two snapshots or a snapshot against latest'
        'break-lock:Remove a stale repository lock'
        'key:Manage encryption key slots'
        'cat:Display raw JSON content of repository objects'
        'completion:Generate shell completion scripts'
        'version:Print version information'
        'help:Show usage information'
    )

    local -a global_flags
    global_flags=(
        '-store[Storage backend URI (local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>)]:uri:'
        '-s3-endpoint[S3 compatible endpoint URL]:url:'
        '-s3-region[S3 region]:region:'
        '-s3-access-key[S3 access key ID]:key:'
        '-s3-secret-key[S3 secret access key]:secret:'
        '-source-sftp-password[SFTP source password]:password:'
        '-source-sftp-key[Path to SSH private key for SFTP source]:key:_files'
        '-store-sftp-password[SFTP store password]:password:'
        '-store-sftp-key[Path to SSH private key for SFTP store]:key:_files'
        '-encryption-key[Platform key (hex-encoded)]:key:'
        '-password[Repository password]:password:'
        '-recovery-key[Recovery key (24-word mnemonic)]:words:'
        '-kms-key-arn[AWS KMS key ARN]:arn:'
        '-kms-region[AWS KMS region]:region:'
        '-kms-endpoint[Custom AWS KMS endpoint]:url:'
        '-disable-packfile[Disable bundling small objects into packs]'
        '-prompt[Prompt for password interactively]'
        '-verbose[Log detailed operations]'
        '-quiet[Suppress progress bars]'
        '-debug[Log every store request]'
    )

    # Check if a subcommand has been given
    local cmd
    local -i i=2
    while (( i < CURRENT )); do
        case "${words[i]}" in
            -*)
                # Skip flags with values
                case "${words[i]}" in
                    -store|-s3-endpoint|-s3-region|-s3-access-key|-s3-secret-key|-source-sftp-password|-source-sftp-key|-store-sftp-password|-store-sftp-key|-encryption-key|-password|-recovery-key|-kms-key-arn|-kms-region|-kms-endpoint|-source|-drive-id|-google-credentials|-google-token-file|-onedrive-client-id|-onedrive-token-file|-tag|-output|-keep-last|-keep-hourly|-keep-daily|-keep-weekly|-keep-monthly|-keep-yearly|-group-by|-account)
                        (( i++ )) ;;
                esac
                ;;
            *)
                cmd="${words[i]}"
                break
                ;;
        esac
        (( i++ ))
    done

    if [[ -z "$cmd" ]]; then
        _describe -t commands 'cloudstic command' commands
        _arguments $global_flags
        return
    fi

    case "$cmd" in
        init)
            _arguments $global_flags \
                '-add-recovery-key[Generate a 24-word recovery key]' \
                '-no-encryption[Create an unencrypted repository]' \
                '-adopt-slots[Adopt existing key slots]'
            ;;
        backup)
            _arguments $global_flags \
                '-source[Source URI]:uri:(local: sftp:// gdrive gdrive-changes onedrive onedrive-changes)' \
                '-drive-id[Shared drive ID]:id:' \
                '-skip-native-files[Exclude Google-native files]' \
                '-google-credentials[Google service account credentials JSON]:path:_files' \
                '-google-token-file[Google OAuth token file]:path:_files' \
                '-onedrive-client-id[OneDrive OAuth client ID]:id:' \
                '-onedrive-token-file[OneDrive OAuth token file]:path:_files' \
                '*-tag[Tag for the snapshot]:tag:' \
                '-dry-run[Scan without writing]'
            ;;
        restore)
            _arguments $global_flags \
                '-output[Output ZIP file path]:path:_files' \
                '-dry-run[Show what would be restored]' \
                ':snapshot ID:'
            ;;
        list)
            _arguments $global_flags \
                '-group[Group output by source identity]'
            ;;
        ls)
            _arguments $global_flags \
                ':snapshot ID:'
            ;;
        prune)
            _arguments $global_flags \
                '-dry-run[Show what would be deleted]'
            ;;
        forget)
            _arguments $global_flags \
                '-prune[Run prune after forgetting]' \
                '-dry-run[Show what would be removed]' \
                '-keep-last[Keep N most recent snapshots]:count:' \
                '-keep-hourly[Keep N hourly snapshots]:count:' \
                '-keep-daily[Keep N daily snapshots]:count:' \
                '-keep-weekly[Keep N weekly snapshots]:count:' \
                '-keep-monthly[Keep N monthly snapshots]:count:' \
                '-keep-yearly[Keep N yearly snapshots]:count:' \
                '*-tag[Filter by tag]:tag:' \
                '-source[Filter by source URI (e.g. local:./docs, gdrive)]:uri:' \
                '-account[Filter by account]:account:' \
                '-group-by[Group snapshots by fields]:fields:' \
                ':snapshot ID:'
            ;;
        diff)
            _arguments $global_flags \
                ':snapshot 1:' \
                ':snapshot 2:'
            ;;
        break-lock)
            _arguments $global_flags
            ;;
        key)
            local -a key_commands
            key_commands=(
                'list:List all encryption key slots'
                'add-recovery:Generate a 24-word recovery key'
                'passwd:Change the repository password'
            )
            # Check if a key subcommand has been given
            local key_sub
            local -i ki=$((i+1))
            while (( ki < CURRENT )); do
                case "${words[ki]}" in
                    -*) ;;
                    *) key_sub="${words[ki]}"; break ;;
                esac
                (( ki++ ))
            done
            if [[ -z "$key_sub" ]]; then
                _describe -t key-commands 'key subcommand' key_commands
                return
            fi
            case "$key_sub" in
                passwd)
                    _arguments $global_flags \
                        '-new-password[New repository password]:password:'
                    ;;
                *)
                    _arguments $global_flags
                    ;;
            esac
            ;;
        cat)
            _arguments $global_flags \
                '-json[Suppress non-JSON output]' \
                '-raw[Output raw, unformatted data]' \
                '*:object key:'
            ;;
        completion)
            _arguments ':shell:(bash zsh fish)'
            ;;
    esac
}

_cloudstic "$@"
`)
}

// completionFish writes a fish completion script to w.
func completionFish(w io.Writer) {
	_, _ = fmt.Fprint(w, `# fish completion for cloudstic

# Disable file completions by default
complete -c cloudstic -f

# Subcommands
complete -c cloudstic -n __fish_use_subcommand -a init -d 'Initialize a new repository'
complete -c cloudstic -n __fish_use_subcommand -a backup -d 'Create a new backup snapshot'
complete -c cloudstic -n __fish_use_subcommand -a restore -d 'Restore files from a snapshot'
complete -c cloudstic -n __fish_use_subcommand -a list -d 'List all backup snapshots'
complete -c cloudstic -n __fish_use_subcommand -a ls -d 'List files within a snapshot'
complete -c cloudstic -n __fish_use_subcommand -a prune -d 'Remove unused data chunks'
complete -c cloudstic -n __fish_use_subcommand -a forget -d 'Remove a snapshot from history'
complete -c cloudstic -n __fish_use_subcommand -a diff -d 'Compare two snapshots'
complete -c cloudstic -n __fish_use_subcommand -a break-lock -d 'Remove a stale repository lock'
complete -c cloudstic -n __fish_use_subcommand -a key -d 'Manage encryption key slots'
complete -c cloudstic -n __fish_use_subcommand -a cat -d 'Display raw JSON of repository objects'
complete -c cloudstic -n __fish_use_subcommand -a completion -d 'Generate shell completion scripts'
complete -c cloudstic -n __fish_use_subcommand -a version -d 'Print version information'
complete -c cloudstic -n __fish_use_subcommand -a help -d 'Show usage information'

# Global flags (available for all subcommands)
complete -c cloudstic -l store -x -d 'Storage backend URI (local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>)'
complete -c cloudstic -l s3-endpoint -x -d 'S3 compatible endpoint URL'
complete -c cloudstic -l s3-region -x -d 'S3 region'
complete -c cloudstic -l s3-access-key -x -d 'S3 access key ID'
complete -c cloudstic -l s3-secret-key -x -d 'S3 secret access key'
complete -c cloudstic -l source-sftp-password -x -d 'SFTP source password'
complete -c cloudstic -l source-sftp-key -r -F -d 'Path to SSH private key for SFTP source'
complete -c cloudstic -l store-sftp-password -x -d 'SFTP store password'
complete -c cloudstic -l store-sftp-key -r -F -d 'Path to SSH private key for SFTP store'
complete -c cloudstic -l encryption-key -x -d 'Platform key (hex-encoded)'
complete -c cloudstic -l password -x -d 'Repository password'
complete -c cloudstic -l recovery-key -x -d 'Recovery key (24-word mnemonic)'
complete -c cloudstic -l kms-key-arn -x -d 'AWS KMS key ARN'
complete -c cloudstic -l kms-region -x -d 'AWS KMS region'
complete -c cloudstic -l kms-endpoint -x -d 'Custom AWS KMS endpoint'
complete -c cloudstic -l disable-packfile -d 'Disable bundling small objects into packs'
complete -c cloudstic -l prompt -d 'Prompt for password interactively'
complete -c cloudstic -l verbose -d 'Log detailed operations'
complete -c cloudstic -l quiet -d 'Suppress progress bars'
complete -c cloudstic -l debug -d 'Log every store request'

# init
complete -c cloudstic -n '__fish_seen_subcommand_from init' -l add-recovery-key -d 'Generate a 24-word recovery key'
complete -c cloudstic -n '__fish_seen_subcommand_from init' -l no-encryption -d 'Create an unencrypted repository'
complete -c cloudstic -n '__fish_seen_subcommand_from init' -l adopt-slots -d 'Adopt existing key slots'

# backup
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l source -x -a 'local: sftp:// gdrive gdrive-changes onedrive onedrive-changes' -d 'Source URI'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l drive-id -x -d 'Shared drive ID'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l skip-native-files -d 'Exclude Google-native files'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l google-credentials -r -F -d 'Google service account credentials JSON'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l google-token-file -r -F -d 'Google OAuth token file'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l onedrive-client-id -x -d 'OneDrive OAuth client ID'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l onedrive-token-file -r -F -d 'OneDrive OAuth token file'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l tag -x -d 'Tag for the snapshot'
complete -c cloudstic -n '__fish_seen_subcommand_from backup' -l dry-run -d 'Scan without writing'

# restore
complete -c cloudstic -n '__fish_seen_subcommand_from restore' -l output -r -F -d 'Output ZIP file path'
complete -c cloudstic -n '__fish_seen_subcommand_from restore' -l dry-run -d 'Show what would be restored'

# list
complete -c cloudstic -n '__fish_seen_subcommand_from list' -l group -d 'Group output by source identity'

# prune
complete -c cloudstic -n '__fish_seen_subcommand_from prune' -l dry-run -d 'Show what would be deleted'

# forget
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l prune -d 'Run prune after forgetting'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l dry-run -d 'Show what would be removed'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l keep-last -x -d 'Keep N most recent snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l keep-hourly -x -d 'Keep N hourly snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l keep-daily -x -d 'Keep N daily snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l keep-weekly -x -d 'Keep N weekly snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l keep-monthly -x -d 'Keep N monthly snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l keep-yearly -x -d 'Keep N yearly snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l tag -x -d 'Filter by tag'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l source -x -d 'Filter by source URI (e.g. local:./docs, gdrive)'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l account -x -d 'Filter by account'
complete -c cloudstic -n '__fish_seen_subcommand_from forget' -l group-by -x -d 'Group snapshots by fields'

# key subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from key; and not __fish_seen_subcommand_from list add-recovery passwd' -a list -d 'List all encryption key slots'
complete -c cloudstic -n '__fish_seen_subcommand_from key; and not __fish_seen_subcommand_from list add-recovery passwd' -a add-recovery -d 'Generate a 24-word recovery key'
complete -c cloudstic -n '__fish_seen_subcommand_from key; and not __fish_seen_subcommand_from list add-recovery passwd' -a passwd -d 'Change the repository password'
complete -c cloudstic -n '__fish_seen_subcommand_from key; and __fish_seen_subcommand_from passwd' -l new-password -x -d 'New repository password'

# cat
complete -c cloudstic -n '__fish_seen_subcommand_from cat' -l json -d 'Suppress non-JSON output'
complete -c cloudstic -n '__fish_seen_subcommand_from cat' -l raw -d 'Output raw, unformatted data'

# completion
complete -c cloudstic -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish' -d 'Shell type'
`)
}
