# shellcheck shell=bash
#
# Shared by the scripts that build throwaway Python environments.
#
# Locate uv by running it, rather than with `command -v`: a sandbox may allow
# exec while denying stat on the directory holding it, which makes `command -v`
# lie. Sourcing this sets UV, or exits with an explanation.

find_uv() {
  local candidate
  for candidate in "${UV:-}" uv "${HOME}/.local/bin/uv" /opt/homebrew/bin/uv \
                   /usr/local/bin/uv "${HOME}/.cargo/bin/uv"; do
    [[ -n "${candidate}" ]] || continue
    if "${candidate}" --version >/dev/null 2>&1; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}

UV="$(find_uv)" || {
  echo "error: uv not found; install it (https://docs.astral.sh/uv/) or set UV=/path/to/uv" >&2
  exit 1
}
