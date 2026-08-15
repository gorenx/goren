#!/usr/bin/env bash

set -euo pipefail

readonly expected_public_https="https://github.com/gorenx/goren.git"
readonly expected_public_ssh="git@github.com:gorenx/goren.git"

usage() {
  cat <<'EOF'
Usage:
  scripts/publish-public.sh prepare [--source <ref>] [--initialize]
  scripts/publish-public.sh push [--source <ref>]
  scripts/publish-public.sh clean [--source <ref>]

prepare
  Rewrites the committed source history in an isolated repository. Markdown and
  internal design paths are removed from every code commit, then the current
  root README.md and README.zh-CN.md are committed once.

push
  Pushes the previously prepared main branch. An initial publication uses an
  exact --force-with-lease guarded by the public main SHA captured by prepare.
  Later publications must be fast-forward updates.

clean
  Removes only the prepared repository for the resolved source commit.
EOF
}

fail() {
  printf 'publish-public: %s\n' "$*" >&2
  exit 1
}

command_name="${1:-}"
if [[ -z "$command_name" ]]; then
  usage
  exit 2
fi
shift

source_ref="HEAD"
initialize="false"

while (($# > 0)); do
  case "$1" in
    --source)
      (($# >= 2)) || fail "--source requires a Git ref"
      source_ref="$2"
      shift 2
      ;;
    --initialize)
      initialize="true"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

case "$command_name" in
  prepare | push | clean) ;;
  *)
    usage
    fail "unknown command: $command_name"
    ;;
esac

repository_root="$(git rev-parse --show-toplevel 2>/dev/null)" || fail "run inside the Goren repository"
source_commit="$(git -C "$repository_root" rev-parse --verify "${source_ref}^{commit}" 2>/dev/null)" || fail "source ref is not a commit: $source_ref"
git_common_dir="$(git -C "$repository_root" rev-parse --path-format=absolute --git-common-dir)"
release_root="$git_common_dir/goren-public-release"
prepared_root="$release_root/$source_commit"
prepared_repository_file="$prepared_root/prepared-repository"
prepared_repository=""

if [[ -f "$prepared_repository_file" ]]; then
  prepared_repository="$(<"$prepared_repository_file")"
fi

case "$prepared_root" in
  "$release_root"/*) ;;
  *) fail "resolved preparation path escaped the Git metadata directory" ;;
esac

if [[ "$command_name" == "clean" ]]; then
  [[ -e "$prepared_root" ]] || {
    printf 'No prepared publication exists for %s.\n' "$source_commit"
    exit 0
  }

  if [[ -n "$prepared_repository" ]]; then
    temporary_base="${TMPDIR:-/tmp}"
    temporary_base="${temporary_base%/}"
    case "$prepared_repository" in
      "$temporary_base"/goren-public-release."$source_commit".*/repository)
        prepared_container="$(dirname "$prepared_repository")"
        rm -rf -- "$prepared_container"
        ;;
      *) fail "recorded preparation path is outside the expected temporary directory: $prepared_repository" ;;
    esac
  fi

  rm -rf -- "$prepared_root"
  printf 'Removed prepared publication for %s.\n' "$source_commit"
  exit 0
fi

public_url="$(git -C "$repository_root" remote get-url origin 2>/dev/null)" || fail "origin remote is missing"
case "$public_url" in
  "$expected_public_https" | "$expected_public_ssh") ;;
  *) fail "origin is not the expected public repository: $public_url" ;;
esac

resolve_public_main() {
  git ls-remote --heads "$public_url" refs/heads/main | awk 'NR == 1 { print $1 }'
}

validate_public_history() {
  local publication_repository="$1"
  local unexpected_documents
  local forbidden_paths
  local unexpected_doc_commits

  unexpected_documents="$({ git -C "$publication_repository" ls-files '*.md' || true; } | grep -Ev '^(README\.md|README\.zh-CN\.md)$' || true)"
  [[ -z "$unexpected_documents" ]] || fail "unexpected Markdown remains in the public tree:\n$unexpected_documents"

  [[ "$(git -C "$publication_repository" ls-files '*.md' | wc -l | tr -d ' ')" == "2" ]] || fail "the public tree must contain exactly the two root README files"

  forbidden_paths="$({ git -C "$publication_repository" log --format= --name-only main || true; } | grep -E '(^|/)(AGENTS\.md|\.env|\.credentials\.json|sessions\.sqlite|workspaces\.sqlite)$|^zh-CN/|^llm/docs/|^\.idea/|(^|/)node_modules/' || true)"
  [[ -z "$forbidden_paths" ]] || fail "forbidden paths remain in public history:\n$forbidden_paths"

  unexpected_doc_commits="$(git -C "$publication_repository" log --format='%s' main | grep -Ei '^(docs|doc)(\([^)]*\))?:' | grep -Fvx 'docs: publish latest Goren README' || true)"
  [[ -z "$unexpected_doc_commits" ]] || fail "documentation commits remain in public history:\n$unexpected_doc_commits"

  [[ "$(git -C "$publication_repository" rev-list --count main -- README.md)" -ge 1 ]] || fail "README.md is absent from public history"
  [[ "$(git -C "$publication_repository" rev-list --count main -- README.zh-CN.md)" -ge 1 ]] || fail "README.zh-CN.md is absent from public history"
}

if [[ "$command_name" == "prepare" ]]; then
  command -v git-filter-repo >/dev/null 2>&1 || fail "git-filter-repo is required"
  command -v go >/dev/null 2>&1 || fail "go is required"
  command -v pnpm >/dev/null 2>&1 || fail "pnpm is required"
  [[ ! -e "$prepared_root" ]] || fail "a prepared publication already exists; inspect it or run clean with the same --source"

  mkdir -p "$prepared_root"
  temporary_base="${TMPDIR:-/tmp}"
  temporary_base="${temporary_base%/}"
  prepared_container="$(mktemp -d "$temporary_base/goren-public-release.${source_commit}.XXXXXX")"
  prepared_repository="$prepared_container/repository"
  printf '%s\n' "$prepared_repository" >"$prepared_repository_file"
  git -C "$repository_root" show "$source_commit:README.md" >"$prepared_root/README.md"
  git -C "$repository_root" show "$source_commit:README.zh-CN.md" >"$prepared_root/README.zh-CN.md"

  git clone --quiet --no-local --no-checkout "$repository_root" "$prepared_repository"
  git -C "$prepared_repository" branch filtered-code "$source_commit"
  git -C "$prepared_repository" symbolic-ref HEAD refs/heads/filtered-code

  while IFS= read -r reference_name; do
    [[ "$reference_name" == "refs/heads/filtered-code" ]] || git -C "$prepared_repository" update-ref -d "$reference_name"
  done < <(git -C "$prepared_repository" for-each-ref --format='%(refname)')
  git -C "$prepared_repository" remote remove origin

  filename_filter=$'lower = filename.lower()\nparts = lower.split(b"/")\nbase = parts[-1]\nif lower.endswith(b".md"): return None\nif lower.startswith(b"zh-cn/") or lower.startswith(b"llm/docs/") or lower.startswith(b".idea/"): return None\nif lower in (b"scripts/publish-public.sh", b"scripts/public-paths.txt"): return None\nif base == b".env" or (base.startswith(b".env.") and base != b".env.example"): return None\nif base in (b".credentials.json", b"sessions.sqlite", b"workspaces.sqlite"): return None\nif b"node_modules" in parts: return None\nreturn filename'
  git -C "$prepared_repository" filter-repo --force --refs refs/heads/filtered-code --filename-callback "$filename_filter" >/dev/null

  public_main_before="$(resolve_public_main)"
  printf '%s\n' "$source_commit" >"$prepared_root/source-commit"
  printf '%s\n' "$public_main_before" >"$prepared_root/expected-public-main"
  printf '%s\n' "$initialize" >"$prepared_root/initialize"

  author_name="$(git -C "$repository_root" config user.name || true)"
  author_email="$(git -C "$repository_root" config user.email || true)"
  [[ -n "$author_name" && -n "$author_email" ]] || fail "Git user.name and user.email are required"
  git -C "$prepared_repository" config user.name "$author_name"
  git -C "$prepared_repository" config user.email "$author_email"
  git -C "$prepared_repository" remote add public "$public_url"

  if [[ "$initialize" == "true" ]]; then
    git -C "$prepared_repository" checkout --quiet -b main filtered-code
  else
    [[ -n "$public_main_before" ]] || fail "public main does not exist; rerun prepare with --initialize"
    git -C "$prepared_repository" fetch --quiet --no-tags public refs/heads/main:refs/remotes/public/main
    git -C "$prepared_repository" checkout --quiet -b main refs/remotes/public/main
    git -C "$prepared_repository" merge-base main filtered-code >/dev/null 2>&1 || fail "public main is not derived from the filtered code history; initial publication requires --initialize"
    if ! git -C "$prepared_repository" merge-base --is-ancestor filtered-code main; then
      git -C "$prepared_repository" merge --no-ff --no-edit -m "release: publish Goren source updates" filtered-code
    fi
  fi

  cp "$prepared_root/README.md" "$prepared_repository/README.md"
  cp "$prepared_root/README.zh-CN.md" "$prepared_repository/README.zh-CN.md"
  git -C "$prepared_repository" add README.md README.zh-CN.md
  if ! git -C "$prepared_repository" diff --cached --quiet; then
    git -C "$prepared_repository" commit --quiet -m "docs: publish latest Goren README"
  fi

  while IFS= read -r reference_name; do
    [[ "$reference_name" == "refs/heads/main" ]] || git -C "$prepared_repository" update-ref -d "$reference_name"
  done < <(git -C "$prepared_repository" for-each-ref --format='%(refname)')
  git -C "$prepared_repository" reflog expire --expire=now --all
  git -C "$prepared_repository" gc --quiet --prune=now

  validate_public_history "$prepared_repository"
  git -C "$prepared_repository" diff --check
  go -C "$prepared_repository" test ./...
  go -C "$prepared_repository" vet ./...
  go -C "$prepared_repository" build ./...
  pnpm -C "$prepared_repository/web" install --frozen-lockfile
  pnpm -C "$prepared_repository/web" run build
  git -C "$prepared_repository" diff --exit-code --check
  git -C "$prepared_repository" diff --exit-code
  [[ -z "$(git -C "$prepared_repository" status --porcelain)" ]] || fail "verification left the prepared repository dirty"

  printf '\nPrepared public history from %s.\n' "$source_commit"
  printf 'Repository: %s\n' "$prepared_repository"
  printf 'Public main before prepare: %s\n' "${public_main_before:-<absent>}"
  printf 'Public main after prepare:  %s\n' "$(git -C "$prepared_repository" rev-parse main)"
  printf 'Commits: %s\n' "$(git -C "$prepared_repository" rev-list --count main)"
  printf '\nInspect with:\n  git -C %q log --oneline --decorate --graph --all\n  git -C %q ls-tree -r --name-only main\n' "$prepared_repository" "$prepared_repository"
  printf '\nPush only after inspection:\n  scripts/publish-public.sh push --source %q\n' "$source_commit"
  exit 0
fi

[[ -d "$prepared_repository/.git" ]] || fail "no prepared publication exists for $source_commit"
recorded_source="$(<"$prepared_root/source-commit")"
[[ "$recorded_source" == "$source_commit" ]] || fail "prepared source does not match $source_commit"
recorded_initialize="$(<"$prepared_root/initialize")"
expected_public_main="$(<"$prepared_root/expected-public-main")"
current_public_main="$(resolve_public_main)"
[[ "$current_public_main" == "$expected_public_main" ]] || fail "public main changed after prepare; expected ${expected_public_main:-<absent>}, found ${current_public_main:-<absent>}"

validate_public_history "$prepared_repository"
[[ -z "$(git -C "$prepared_repository" status --porcelain)" ]] || fail "prepared repository is dirty"

if [[ "$recorded_initialize" == "true" ]]; then
  git -C "$prepared_repository" push --force-with-lease="refs/heads/main:$expected_public_main" public main:main
else
  git -C "$prepared_repository" push public main:main
fi

published_commit="$(git -C "$prepared_repository" rev-parse main)"
printf 'Published %s to %s main.\n' "$published_commit" "$public_url"
