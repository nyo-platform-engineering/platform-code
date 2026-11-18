#!/usr/bin/env bash
# Update only AAT image pins, with temporary Git authentication and no force-push.
set -euo pipefail
set +x

# Keep the short-lived token out of later Python subprocess environments.
# Never place it in a remote URL, command argument, or persistent Git config.
git_token="${GITOPS_TOKEN:?Missing temporary GitHub token}"
unset GITOPS_TOKEN

if [[ ! "$GITHUB_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo 'Unexpected GitHub repository name.' >&2
  exit 1
fi
repository_url="https://github.com/$GITHUB_REPOSITORY.git"
image_directory=code/infrastructure/argo-apps/dev/aat

# This file contains instructions, not the token. Only Git receives its token
# environment while it authenticates to the fixed GitHub HTTPS destination.
askpass_script="$(mktemp "$RUNNER_TEMP/aat-git-auth.XXXXXX")"
trap 'rm -f "$askpass_script"' EXIT
cat > "$askpass_script" <<'ASKPASS'
#!/usr/bin/env bash
case "$1" in
  *Username*) printf '%s\n' 'x-access-token' ;;
  *Password*) printf '%s\n' "$GITOPS_TOKEN" ;;
  *) exit 1 ;;
esac
ASKPASS
chmod 700 "$askpass_script"

git_remote() {
  GITOPS_TOKEN="$git_token" GIT_ASKPASS="$askpass_script" GIT_TERMINAL_PROMPT=0 \
    git -c credential.helper= -c core.hooksPath=/dev/null "$@"
}

check_staged_paths() {
  # Fail closed if any generated change escapes these image.yaml files.
  local file
  while IFS= read -r file; do
    case "$file" in
      "$image_directory/aggregator/image.yaml"|\
      "$image_directory/citus/image.yaml"|\
      "$image_directory/auth/image.yaml"|\
      "$image_directory/client/image.yaml"|\
      "$image_directory/bmkg/image.yaml"|\
      "$image_directory/pvmbg/image.yaml"|\
      "$image_directory/dashboard-updater/image.yaml"|\
      "$image_directory/field-notifier/image.yaml") ;;
      *)
        echo "Refusing unexpected staged path: $file" >&2
        exit 1
        ;;
    esac
  done < <(git diff --cached --name-only)
}

git config user.name 'github-actions[bot]'
git config user.email '41898282+github-actions[bot]@users.noreply.github.com'
git config core.hooksPath /dev/null

# Main may move during a build. Recompute freshness after each competing push,
# rather than rebasing a generated commit that might now reference stale code.
for attempt in 1 2 3 4 5; do
  git_remote fetch --no-tags "$repository_url" refs/heads/main:refs/remotes/origin/main
  git checkout -B aat-image-update origin/main
  python3 .github/scripts/aat/update_gitops_image_pins.py
  git add -- "$image_directory"/*/image.yaml
  check_staged_paths
  if git diff --cached --quiet; then
    echo 'No image pins need updating.'
    exit 0
  fi
  git commit -m "chore(aat): update image digests for ${BUILD_COMMIT:0:12}"
  if git_remote push "$repository_url" HEAD:main; then
    exit 0
  fi
  echo "Push attempt $attempt failed; retrying against current main."
  sleep 2
done

echo 'Unable to push image pins. Check branch protection and workflow permissions.' >&2
exit 1
