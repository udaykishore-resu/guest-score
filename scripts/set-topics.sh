#!/usr/bin/env bash
# Set this repository's GitHub topics.
#
# Topics are repository metadata, not files, so they cannot be committed — they
# have to be pushed through the API. Keeping the list here means the intended
# set is reviewable in the diff even though the effect lives on github.com.
#
#   ./scripts/set-topics.sh              # apply to udaykishore-resu/guest-score
#   ./scripts/set-topics.sh owner/repo   # apply somewhere else
#   ./scripts/set-topics.sh --print      # just show what would be set
#
# Requires the GitHub CLI, authenticated: gh auth login
set -euo pipefail

REPO_DEFAULT="udaykishore-resu/guest-score"

# GitHub allows at most 20 topics. Each must be lowercase, and may contain
# letters, numbers and hyphens only.
TOPICS=(
  # what it is
  guest-scoring
  reputation-system
  credit-scoring
  hospitality
  hotel-management
  identity-verification

  # how it is built
  golang
  react
  typescript
  grpc
  graphql
  rest-api

  # what it runs on
  postgresql
  redis
  elasticsearch
  mqtt
  kubernetes
  docker
  microservices
  spec-driven-development
)

if [ "${1:-}" = "--print" ]; then
  printf '%s\n' "${TOPICS[@]}"
  echo "(${#TOPICS[@]} topics)"
  exit 0
fi

REPO="${1:-$REPO_DEFAULT}"

if [ "${#TOPICS[@]}" -gt 20 ]; then
  echo "error: GitHub accepts at most 20 topics, this list has ${#TOPICS[@]}" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "error: the GitHub CLI is not installed — see https://cli.github.com" >&2
  exit 1
fi

args=()
for t in "${TOPICS[@]}"; do
  args+=(--add-topic "$t")
done

echo "Setting ${#TOPICS[@]} topics on $REPO"
gh repo edit "$REPO" "${args[@]}"
echo "Done. Current topics:"
gh repo view "$REPO" --json repositoryTopics \
  --jq '.repositoryTopics[].name' 2>/dev/null | sort | sed 's/^/  /'
