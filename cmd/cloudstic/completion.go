package main

import (
	"context"
	"fmt"
	"io"
)

func runCompletion(r *runner) int {
	if len(r.args) < 1 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic completion <shell>")
		_, _ = fmt.Fprintln(r.errOut)
		_, _ = fmt.Fprintln(r.errOut, "Available shells: bash, zsh, fish")
		_, _ = fmt.Fprintln(r.errOut)
		_, _ = fmt.Fprintln(r.errOut, "Setup:")
		_, _ = fmt.Fprintln(r.errOut, "  bash:  source <(cloudstic completion bash)")
		_, _ = fmt.Fprintln(r.errOut, "  zsh:   source <(cloudstic completion zsh)")
		_, _ = fmt.Fprintln(r.errOut, "  fish:  cloudstic completion fish | source")
		return 1
	}

	shell := r.args[0]
	switch shell {
	case "bash":
		completionBash(r.out)
	case "zsh":
		completionZsh(r.out)
	case "fish":
		completionFish(r.out)
	default:
		return r.fail("Unsupported shell: %s\nAvailable shells: bash, zsh, fish", shell)
	}
	return 0
}

// completionBash writes a bash completion script to w.
func completionBash(w io.Writer) {
	_, _ = fmt.Fprint(w, renderCompletion(`# bash completion for cloudstic

_cloudstic_query() {
    local kind="$1"
    local cur="$2"
    shift 2
    cloudstic __complete "$kind" "$cur" "$@" 2>/dev/null
}

_cloudstic() {
    local cur prev words cword
    _init_completion || return

    local commands="@@COMMAND_NAMES@@"

    local global_flags="@@GLOBAL_FLAGS@@"

    # Identify the subcommand
    local cmd=""
    local i
    for ((i=1; i < cword; i++)); do
        case "${words[i]}" in
            -*)
                # skip flags and their values
                case "${words[i]}" in
					@@GLOBAL_VALUE_FLAGS@@|-source|-auth-ref|-google-credentials|-google-credentials-ref|-google-credentials-json|-google-token-file|-google-token-ref|-onedrive-client-id|-onedrive-token-file|-onedrive-token-ref|-tag|-output|-format|-path|-keep-last|-keep-hourly|-keep-daily|-keep-weekly|-keep-monthly|-keep-yearly|-group-by|-account|-xattr-namespaces|-exclude|-exclude-file|-new-password|-name|-provider|-uri|-store-ref)
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
        case "$prev" in
            -profile)
                COMPREPLY=($(compgen -W "$(_cloudstic_query profile-names "$cur" "${words[@]:1:$((cword-1))}")" -- "$cur"))
                return ;;
            -store)
                COMPREPLY=($(compgen -W "local: s3: b2: sftp://" -- "$cur"))
                return ;;
            -source-sftp-key|-store-sftp-key|-output|-profiles-file)
                _filedir
                return ;;
        esac
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi

    # Complete flags per subcommand
    local cmd_flags=""
    case "$cmd" in
@@BASH_CMD_FLAGS@@
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
        profile)
            local profile_sub=""
            local j
            for ((j=i+1; j < cword; j++)); do
                case "${words[j]}" in
                    -*) ;;
                    *) profile_sub="${words[j]}"; break ;;
                esac
            done
            if [[ -z "$profile_sub" ]]; then
                COMPREPLY=($(compgen -W "list show new" -- "$cur"))
                return
            fi
            case "$profile_sub" in
                list)
                    cmd_flags="-profiles-file" ;;
                show)
                    cmd_flags="-profiles-file" ;;
                new)
                    cmd_flags="-profiles-file -name -source -store-ref -store -auth-ref -tag -exclude -exclude-file -ignore-empty-snapshot -skip-native-files -volume-uuid -google-credentials -google-credentials-ref -google-credentials-json -google-token-file -google-token-ref -onedrive-client-id -onedrive-token-file -onedrive-token-ref" ;;
                *)
                    cmd_flags="" ;;
            esac
            ;;
        auth)
            local auth_sub=""
            local j
            for ((j=i+1; j < cword; j++)); do
                case "${words[j]}" in
                    -*) ;;
                    *) auth_sub="${words[j]}"; break ;;
                esac
            done
            if [[ -z "$auth_sub" ]]; then
                COMPREPLY=($(compgen -W "list show new login" -- "$cur"))
                return
            fi
            case "$auth_sub" in
                list)
                    cmd_flags="-profiles-file" ;;
                show)
                    cmd_flags="-profiles-file" ;;
                new)
                    cmd_flags="-profiles-file -name -provider -google-credentials -google-credentials-ref -google-credentials-json -google-token-file -google-token-ref -onedrive-client-id -onedrive-token-file -onedrive-token-ref" ;;
                login)
                    cmd_flags="-profiles-file -name" ;;
                *)
                    cmd_flags="" ;;
            esac
            ;;
        store)
            local store_sub=""
            local j
            for ((j=i+1; j < cword; j++)); do
                case "${words[j]}" in
                    -*) ;;
                    *) store_sub="${words[j]}"; break ;;
                esac
            done
		if [[ -z "$store_sub" ]]; then
			COMPREPLY=($(compgen -W "list show new verify init" -- "$cur"))
			return
		fi
		case "$store_sub" in
			list)
				cmd_flags="-profiles-file" ;;
			show)
				cmd_flags="-profiles-file" ;;
			verify)
				cmd_flags="-profiles-file" ;;
			init)
				cmd_flags="-profiles-file -yes" ;;
			new)
				cmd_flags="-profiles-file -name -uri -s3-region -s3-profile -s3-endpoint -s3-access-key -s3-secret-key -s3-access-key-secret -s3-secret-key-secret -store-sftp-password -store-sftp-key -store-sftp-known-hosts -store-sftp-insecure -store-sftp-password-secret -store-sftp-key-secret -password-secret -encryption-key-secret -recovery-key-secret -kms-key-arn -kms-region -kms-endpoint" ;;
                *)
                    cmd_flags="" ;;
            esac
            ;;
        source)
            local source_sub=""
            local j
            for ((j=i+1; j < cword; j++)); do
                case "${words[j]}" in
                    -*) ;;
                    *) source_sub="${words[j]}"; break ;;
                esac
            done
            if [[ -z "$source_sub" ]]; then
                COMPREPLY=($(compgen -W "discover" -- "$cur"))
                return
            fi
            case "$source_sub" in
                discover)
                    cmd_flags="-portable-only -json" ;;
                *)
                    cmd_flags="" ;;
            esac
            ;;
        setup)
            local setup_sub=""
            local j
            for ((j=i+1; j < cword; j++)); do
                case "${words[j]}" in
                    -*) ;;
                    *) setup_sub="${words[j]}"; break ;;
                esac
            done
            if [[ -z "$setup_sub" ]]; then
                COMPREPLY=($(compgen -W "workstation" -- "$cur"))
                return
            fi
            case "$setup_sub" in
                workstation)
                    cmd_flags="-dry-run -yes -profiles-file -store-ref -json" ;;
                *)
                    cmd_flags="" ;;
            esac
            ;;
        check)
            cmd_flags="-read-data" ;;
        ls|diff|break-lock|version|help)
            cmd_flags="" ;;
    esac

    case "$prev" in
        -profile)
            COMPREPLY=($(compgen -W "$(_cloudstic_query profile-names "$cur" "${words[@]:1:$((cword-1))}")" -- "$cur"))
            return ;;
        -auth-ref)
            COMPREPLY=($(compgen -W "$(_cloudstic_query auth-names "$cur" "${words[@]:1:$((cword-1))}")" -- "$cur"))
            return ;;
        -store)
            # URI completion hint: show scheme prefixes
            COMPREPLY=($(compgen -W "local: s3: b2: sftp://" -- "$cur"))
            return ;;
        -source)
            # URI completion hint: show scheme prefixes and bare keywords
            COMPREPLY=($(compgen -W "local: sftp:// gdrive gdrive-changes onedrive onedrive-changes" -- "$cur"))
            return ;;
        -source-sftp-key|-store-sftp-key|-output|-profiles-file)
            _filedir
            return ;;
    esac

    if [[ -z "$cur" || "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "$cmd_flags $global_flags" -- "$cur"))
        return
    fi
}

complete -F _cloudstic cloudstic
`))
}

// completionZsh writes a zsh completion script to w.
func completionZsh(w io.Writer) {
	_, _ = fmt.Fprint(w, renderCompletion(`#compdef cloudstic

# zsh completion for cloudstic

_cloudstic_query() {
    local kind="$1"
    shift
    local -a prior_words
    prior_words=("${words[@]:2:$(( CURRENT - 2 ))}")
    cloudstic __complete "$kind" "$PREFIX" "${prior_words[@]}" 2>/dev/null
}

_cloudstic_dynamic_values() {
    local kind="$1"
    local label="$2"
    local -a values
    values=(${(f)"$(_cloudstic_query "$kind")"})
    _describe -t "$kind" "$label" values
}

_cloudstic_profile_names() {
    _cloudstic_dynamic_values profile-names 'profile'
}

_cloudstic_auth_names() {
    _cloudstic_dynamic_values auth-names 'auth entry'
}

_cloudstic_store_prefixes() {
    local -a values
    values=('local:' 's3:' 'b2:' 'sftp://')
    compadd -Q -S '' -X 'store URI prefix' -- "${values[@]}"
}

_cloudstic_source_prefixes() {
    local -a values
    values=('local:' 'sftp://' 'gdrive' 'gdrive-changes' 'onedrive' 'onedrive-changes')
    compadd -Q -S '' -X 'source URI prefix' -- "${values[@]}"
}

_cloudstic() {
    local -a commands
    commands=(
@@ZSH_COMMANDS@@
    )

    local prev_word="${words[CURRENT-1]}"

    local -a global_flags
    global_flags=(
@@ZSH_GLOBAL_FLAGS@@
    )
    # Check if a subcommand has been given
    local cmd
    local -i i=2
    while (( i < CURRENT )); do
        case "${words[i]}" in
            -*)
                # Skip flags with values
                case "${words[i]}" in
					-store|-profile|-profiles-file|-s3-endpoint|-s3-region|-s3-profile|-s3-access-key|-s3-secret-key|-source-sftp-password|-source-sftp-key|-store-sftp-password|-store-sftp-key|-encryption-key|-password|-recovery-key|-kms-key-arn|-kms-region|-kms-endpoint|-source|-auth-ref|-google-credentials|-google-credentials-ref|-google-credentials-json|-google-token-file|-google-token-ref|-onedrive-client-id|-onedrive-token-file|-onedrive-token-ref|-tag|-output|-keep-last|-keep-hourly|-keep-daily|-keep-weekly|-keep-monthly|-keep-yearly|-group-by|-account)
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
        case "$prev_word" in
            @@GLOBAL_VALUE_FLAGS@@)
                _arguments $global_flags
                return
                ;;
        esac
        _describe -t commands 'cloudstic command' commands
        _arguments $global_flags
        return
    fi

    case "$cmd" in
@@ZSH_CMD_FLAGS@@
        profile)
            local -a profile_commands
            profile_commands=(
                'list:List stores, auth entries, and backup profiles'
                'show:Show one profile and resolved store/auth references'
                'new:Create or update a backup profile'
            )
            local profile_sub
            local -i pi=$((i+1))
            while (( pi < CURRENT )); do
                case "${words[pi]}" in
                    -*) ;;
                    *) profile_sub="${words[pi]}"; break ;;
                esac
                (( pi++ ))
            done
            if [[ -z "$profile_sub" ]]; then
                _describe -t profile-commands 'profile subcommand' profile_commands
                return
            fi
            case "$profile_sub" in
                list)
                    _arguments '-profiles-file[Path to profiles YAML file]:path:_files'
                    ;;
                show)
                    _arguments '-profiles-file[Path to profiles YAML file]:path:_files' ':profile name:'
                    ;;
                new)
                    _arguments \
                        '-profiles-file[Path to profiles YAML file]:path:_files' \
                        '-name[Profile name]:name:' \
                        '-source[Source URI]:uri:(local: sftp:// gdrive gdrive-changes onedrive onedrive-changes)' \
                        '-store-ref[Store reference name]:name:' \
                        '-store[Store URI]:uri:' \
                        '-auth-ref[Auth reference name]:name:_cloudstic_auth_names' \
                        '*-tag[Tag for snapshots]:tag:' \
                        '*-exclude[Exclude pattern]:pattern:' \
                        '-exclude-file[Path to exclude file]:path:_files' \
                        '-ignore-empty-snapshot[Skip creating a new snapshot when nothing changed]' \
                        '-skip-native-files[Exclude Google-native files]' \
                        '-volume-uuid[Volume UUID override]:uuid:' \
                        '-google-credentials[Google service account credentials JSON]:path:_files' \
                        '-google-credentials-ref[Secret reference to Google credentials]:ref:' \
                        '-google-credentials-json[Inline Google credentials JSON]:json:' \
                        '-google-token-file[Google OAuth token file]:path:_files' \
                        '-google-token-ref[Secret reference to Google OAuth token]:ref:' \
                        '-onedrive-client-id[OneDrive OAuth client ID]:id:' \
                        '-onedrive-token-file[OneDrive OAuth token file]:path:_files' \
                        '-onedrive-token-ref[Secret reference to OneDrive OAuth token]:ref:'
                    ;;
                *)
                    _arguments
                    ;;
            esac
            ;;
        auth)
            local -a auth_commands
            auth_commands=(
                'list:List auth entries from profiles.yaml'
                'show:Show one auth entry'
                'new:Create or update a reusable cloud auth entry'
                'login:Run OAuth login flow for one auth entry'
            )
            local auth_sub
            local -i ai=$((i+1))
            while (( ai < CURRENT )); do
                case "${words[ai]}" in
                    -*) ;;
                    *) auth_sub="${words[ai]}"; break ;;
                esac
                (( ai++ ))
            done
            if [[ -z "$auth_sub" ]]; then
                _describe -t auth-commands 'auth subcommand' auth_commands
                return
            fi
            case "$auth_sub" in
                list)
                    _arguments '-profiles-file[Path to profiles YAML file]:path:_files'
                    ;;
                show)
                    _arguments '-profiles-file[Path to profiles YAML file]:path:_files' ':auth name:'
                    ;;
                new)
                    _arguments \
                        '-profiles-file[Path to profiles YAML file]:path:_files' \
                        '-name[Auth reference name]:name:' \
                        '-provider[Auth provider]:provider:(google onedrive)' \
                        '-google-credentials[Google service account credentials JSON]:path:_files' \
                        '-google-credentials-json[Inline Google credentials JSON]:json:' \
                        '-google-token-file[Google OAuth token file]:path:_files' \
                        '-onedrive-client-id[OneDrive OAuth client ID]:id:' \
                        '-onedrive-token-file[OneDrive OAuth token file]:path:_files'
                    ;;
                login)
                    _arguments \
                        '-profiles-file[Path to profiles YAML file]:path:_files' \
                        '-name[Auth reference name]:name:'
                    ;;
                *)
                    _arguments
                    ;;
            esac
            ;;
        store)
            local -a store_commands
			store_commands=(
				'list:List configured stores'
				'show:Show one store and its configuration'
				'new:Create or update a store entry'
				'verify:Verify store credentials and connectivity'
				'init:Initialize a store by reference'
			)
            local store_sub
            local -i si=$((i+1))
            while (( si < CURRENT )); do
                case "${words[si]}" in
                    -*) ;;
                    *) store_sub="${words[si]}"; break ;;
                esac
                (( si++ ))
            done
            if [[ -z "$store_sub" ]]; then
                _describe -t store-commands 'store subcommand' store_commands
                return
            fi
			case "$store_sub" in
				list)
					_arguments '-profiles-file[Path to profiles YAML file]:path:_files'
					;;
				show)
					_arguments '-profiles-file[Path to profiles YAML file]:path:_files' ':store name:'
					;;
				verify)
					_arguments '-profiles-file[Path to profiles YAML file]:path:_files' ':store name:'
					;;
				init)
					_arguments '-profiles-file[Path to profiles YAML file]:path:_files' '-yes[Initialize without confirmation prompt]' ':store name:'
					;;
				new)
                    _arguments \
                        '-profiles-file[Path to profiles YAML file]:path:_files' \
                        '-name[Store reference name]:name:' \
                        '-uri[Store URI]:uri:' \
                        '-s3-region[S3 region]:region:' \
                        '-s3-profile[AWS shared config profile]:profile:' \
                        '-s3-endpoint[S3-compatible endpoint URL]:url:' \
                        '-s3-access-key[S3 static access key]:key:' \
                        '-s3-secret-key[S3 static secret key]:key:' \
                        '-s3-access-key-secret[Secret ref for S3 access key]:ref:' \
                        '-s3-secret-key-secret[Secret ref for S3 secret key]:ref:' \
                        '-store-sftp-password[SFTP password]:password:' \
                        '-store-sftp-key[SFTP private key path]:path:_files' \
                        '-store-sftp-known-hosts[SFTP known_hosts path]:path:_files' \
                        '-store-sftp-insecure[Skip SFTP host key validation (INSECURE)]' \
                        '-store-sftp-password-secret[Secret ref for SFTP password]:ref:' \
                        '-store-sftp-key-secret[Secret ref for SFTP key path]:ref:' \
                        '-password-secret[Secret ref for repository password]:ref:' \
                        '-encryption-key-secret[Secret ref for platform key]:ref:' \
                        '-recovery-key-secret[Secret ref for recovery key mnemonic]:ref:' \
                        '-kms-key-arn[AWS KMS key ARN]:arn:' \
                        '-kms-region[AWS KMS region]:region:' \
                        '-kms-endpoint[Custom KMS endpoint URL]:url:'
                    ;;
                *)
                    _arguments
                    ;;
            esac
            ;;
        source)
            local -a source_commands
            source_commands=(
                'discover:Discover local source candidates'
            )
            local source_sub
            local -i soi=$((i+1))
            while (( soi < CURRENT )); do
                case "${words[soi]}" in
                    -*) ;;
                    *) source_sub="${words[soi]}"; break ;;
                esac
                (( soi++ ))
            done
            if [[ -z "$source_sub" ]]; then
                _describe -t source-commands 'source subcommand' source_commands
                return
            fi
            case "$source_sub" in
                discover)
                    _arguments '-portable-only[Only show portable or external source candidates]' '-json[Write discovered sources as JSON]'
                    ;;
                *)
                    _arguments
                    ;;
            esac
            ;;
        setup)
            local -a setup_commands
            setup_commands=(
                'workstation:Preview workstation onboarding plan'
            )
            local setup_sub
            local -i sui=$((i+1))
            while (( sui < CURRENT )); do
                case "${words[sui]}" in
                    -*) ;;
                    *) setup_sub="${words[sui]}"; break ;;
                esac
                (( sui++ ))
            done
            if [[ -z "$setup_sub" ]]; then
                _describe -t setup-commands 'setup subcommand' setup_commands
                return
            fi
            case "$setup_sub" in
                workstation)
                    _arguments \
                        '-dry-run[Preview generated profiles without writing configuration]' \
                        '-yes[Accept default selections without prompting]' \
                        '-profiles-file[Path to profiles YAML file]:path:_files' \
                        '-store-ref[Existing store reference to attach]:name:' \
                        '-json[Write onboarding plan as JSON]'
                    ;;
                *)
                    _arguments
                    ;;
            esac
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
        completion)
            _arguments ':shell:(bash zsh fish)'
            ;;
    esac
}

compdef _cloudstic cloudstic
`))
}

// completionFish writes a fish completion script to w.
func completionFish(w io.Writer) {
	_, _ = fmt.Fprint(w, renderCompletion(`# fish completion for cloudstic

function __fish_cloudstic_query
    set -l kind $argv[1]
    cloudstic __complete $kind (commandline -ct) (commandline -opc) 2>/dev/null
end

# Disable file completions by default
complete -c cloudstic -f

# Subcommands
@@FISH_COMMANDS@@

# Global flags (available for all subcommands)
@@FISH_GLOBAL_FLAGS@@
complete -c cloudstic -l s3-endpoint -x -d 'S3 compatible endpoint URL'
complete -c cloudstic -l s3-region -x -d 'S3 region'
complete -c cloudstic -l s3-profile -x -d 'AWS shared config profile for S3 auth'
complete -c cloudstic -l s3-access-key -x -d 'S3 access key ID'
complete -c cloudstic -l s3-secret-key -x -d 'S3 secret access key'
complete -c cloudstic -l source-sftp-password -x -d 'SFTP source password'
complete -c cloudstic -l source-sftp-key -r -F -d 'Path to SSH private key for SFTP source'
complete -c cloudstic -l source-sftp-known-hosts -r -F -d 'Path to known_hosts file for SFTP source'
complete -c cloudstic -l source-sftp-insecure -d 'Skip host key validation for SFTP source (INSECURE)'
complete -c cloudstic -l store-sftp-password -x -d 'SFTP store password'
complete -c cloudstic -l store-sftp-key -r -F -d 'Path to SSH private key for SFTP store'
complete -c cloudstic -l store-sftp-known-hosts -r -F -d 'Path to known_hosts file for SFTP store'
complete -c cloudstic -l store-sftp-insecure -d 'Skip host key validation for SFTP store (INSECURE)'
complete -c cloudstic -l encryption-key -x -d 'Platform key (hex-encoded)'
complete -c cloudstic -l password -x -d 'Repository password'
complete -c cloudstic -l recovery-key -x -d 'Recovery key (24-word mnemonic)'
complete -c cloudstic -l kms-key-arn -x -d 'AWS KMS key ARN'
complete -c cloudstic -l kms-region -x -d 'AWS KMS region'
complete -c cloudstic -l kms-endpoint -x -d 'Custom AWS KMS endpoint'
complete -c cloudstic -l disable-packfile -d 'Disable bundling small objects into packs'
complete -c cloudstic -l prompt -d 'Prompt for password interactively'
complete -c cloudstic -l no-prompt -d 'Disable interactive prompts (for scripts and CI)'
complete -c cloudstic -l verbose -d 'Log detailed operations'
complete -c cloudstic -l quiet -d 'Suppress progress bars'
complete -c cloudstic -l json -d 'Write command result as JSON to stdout'
complete -c cloudstic -l debug -d 'Log every store request'

# init
@@FISH_CMD_FLAGS@@

# backup

# profile subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and not __fish_seen_subcommand_from list show new' -a list -d 'List stores, auth entries, and backup profiles'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and not __fish_seen_subcommand_from list show new' -a show -d 'Show one profile and resolved refs'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and not __fish_seen_subcommand_from list show new' -a new -d 'Create or update backup profile'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from list' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from show' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l name -x -d 'Profile name'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l source -x -a 'local: sftp:// gdrive gdrive-changes onedrive onedrive-changes' -d 'Source URI'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l store-ref -x -d 'Store reference name'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l store -x -d 'Store URI'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l auth-ref -x -a '(__fish_cloudstic_query auth-names)' -d 'Auth reference name'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l tag -x -d 'Tag for snapshots'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l exclude -x -d 'Exclude pattern'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l exclude-file -r -F -d 'Path to exclude file'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l ignore-empty-snapshot -d 'Skip creating a new snapshot when nothing changed'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l skip-native-files -d 'Exclude Google-native files'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l volume-uuid -x -d 'Volume UUID override'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l google-credentials -r -F -d 'Google service account credentials JSON'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l google-credentials-ref -x -d 'Secret reference to Google credentials'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l google-credentials-json -x -d 'Inline Google credentials JSON'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l google-token-file -r -F -d 'Google OAuth token file'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l google-token-ref -x -d 'Secret reference to Google OAuth token'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l onedrive-client-id -x -d 'OneDrive OAuth client ID'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l onedrive-token-file -r -F -d 'OneDrive OAuth token file'
complete -c cloudstic -n '__fish_seen_subcommand_from profile; and __fish_seen_subcommand_from new' -l onedrive-token-ref -x -d 'Secret reference to OneDrive OAuth token'

# store subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from store; and not __fish_seen_subcommand_from list show new verify init' -a list -d 'List configured stores'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and not __fish_seen_subcommand_from list show new verify init' -a show -d 'Show one store and its configuration'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and not __fish_seen_subcommand_from list show new verify init' -a new -d 'Create or update a store entry'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and not __fish_seen_subcommand_from list show new verify init' -a verify -d 'Verify store credentials and connectivity'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and not __fish_seen_subcommand_from list show new verify init' -a init -d 'Initialize a store by reference'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from list' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from show' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from new' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from new' -l store-sftp-password -x -d 'SFTP store password'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from new' -l store-sftp-key -r -F -d 'Path to SSH private key for SFTP store'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from new' -l store-sftp-known-hosts -r -F -d 'Path to known_hosts file for SFTP store'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from new' -l store-sftp-insecure -d 'Skip host key validation for SFTP store (INSECURE)'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from verify' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from init' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from store; and __fish_seen_subcommand_from init' -l yes -d 'Initialize without confirmation prompt'

# source subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from source; and not __fish_seen_subcommand_from discover' -a discover -d 'Discover local source candidates'
complete -c cloudstic -n '__fish_seen_subcommand_from source; and __fish_seen_subcommand_from discover' -l portable-only -d 'Only show portable or external source candidates'
complete -c cloudstic -n '__fish_seen_subcommand_from source; and __fish_seen_subcommand_from discover' -l json -d 'Write discovered sources as JSON'

# setup subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from setup; and not __fish_seen_subcommand_from workstation' -a workstation -d 'Preview workstation onboarding plan'
complete -c cloudstic -n '__fish_seen_subcommand_from setup; and __fish_seen_subcommand_from workstation' -l dry-run -d 'Preview generated profiles without writing configuration'
complete -c cloudstic -n '__fish_seen_subcommand_from setup; and __fish_seen_subcommand_from workstation' -l yes -d 'Accept default selections without prompting'
complete -c cloudstic -n '__fish_seen_subcommand_from setup; and __fish_seen_subcommand_from workstation' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from setup; and __fish_seen_subcommand_from workstation' -l store-ref -x -d 'Existing store reference to attach'
complete -c cloudstic -n '__fish_seen_subcommand_from setup; and __fish_seen_subcommand_from workstation' -l json -d 'Write onboarding plan as JSON'

# auth subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and not __fish_seen_subcommand_from list show new login' -a list -d 'List auth entries from profiles.yaml'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and not __fish_seen_subcommand_from list show new login' -a show -d 'Show one auth entry'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and not __fish_seen_subcommand_from list show new login' -a new -d 'Create or update reusable auth entry'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and not __fish_seen_subcommand_from list show new login' -a login -d 'Run OAuth login flow for auth entry'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from list' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from show' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l name -x -d 'Auth reference name'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l provider -x -a 'google onedrive' -d 'Auth provider'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l google-credentials -r -F -d 'Google service account credentials JSON'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l google-credentials-ref -x -d 'Secret reference to Google credentials'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l google-credentials-json -x -d 'Inline Google credentials JSON'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l google-token-file -r -F -d 'Google OAuth token file'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l google-token-ref -x -d 'Secret reference to Google OAuth token'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l onedrive-client-id -x -d 'OneDrive OAuth client ID'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l onedrive-token-file -r -F -d 'OneDrive OAuth token file'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from new' -l onedrive-token-ref -x -d 'Secret reference to OneDrive OAuth token'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from login' -l profiles-file -r -F -d 'Path to profiles YAML file'
complete -c cloudstic -n '__fish_seen_subcommand_from auth; and __fish_seen_subcommand_from login' -l name -x -d 'Auth reference name'

# restore

# list

# prune

# forget

# key subcommands
complete -c cloudstic -n '__fish_seen_subcommand_from key; and not __fish_seen_subcommand_from list add-recovery passwd' -a list -d 'List all encryption key slots'
complete -c cloudstic -n '__fish_seen_subcommand_from key; and not __fish_seen_subcommand_from list add-recovery passwd' -a add-recovery -d 'Generate a 24-word recovery key'
complete -c cloudstic -n '__fish_seen_subcommand_from key; and not __fish_seen_subcommand_from list add-recovery passwd' -a passwd -d 'Change the repository password'
complete -c cloudstic -n '__fish_seen_subcommand_from key; and __fish_seen_subcommand_from passwd' -l new-password -x -d 'New repository password'

# cat

# completion
complete -c cloudstic -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish' -d 'Shell type'
`))
}

// completionCommand declares the `completion` command.
func completionCommand() command {
	return leaf("completion", "Generate shell completion scripts (bash, zsh, fish)",
		func(r *runner, _ context.Context) int { return runCompletion(r) })
}
