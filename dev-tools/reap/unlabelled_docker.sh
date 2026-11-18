#!/usr/bin/env bash
# unlabelled_docker.sh <file>... -- prints file:line:command for every docker
# command in the given shell files that makes something outliving the command
# without the reap label (dev-tools/reap/reap_lib.sh), one per line:
#   docker network create, docker create, docker build, and docker run that
#   is detached (-d) or not --rm.
# A command is a line with its backslash continuations joined; comments are
# skipped. The label is any --label whose value names REAP_COMPOSE_LABEL or
# *REAP_LABEL (the variable suites hold it in).
set -euo pipefail

awk '
function check(cmd, line,    flagged) {
  flagged = 0
  if (cmd ~ /docker network create([[:space:]]|$)/) flagged = 1
  else if (cmd ~ /docker create([[:space:]]|$)/) flagged = 1
  else if (cmd ~ /docker build([[:space:]]|$)/) flagged = 1
  else if (cmd ~ /docker run([[:space:]]|$)/) {
    if (cmd !~ /[[:space:]]--rm([[:space:]]|$)/ || cmd ~ /[[:space:]](-d|--detach)([[:space:]]|$)/) flagged = 1
  }
  if (flagged && cmd !~ /--label[ =]["]?[$][{]?[A-Za-z_]*(REAP_COMPOSE_LABEL|REAP_LABEL)/) {
    gsub(/[[:space:]]+/, " ", cmd)
    print FILENAME ":" line ": " cmd
  }
}
FNR == 1 { pending = ""; start = 0 }
{
  text = $0
  if (pending == "") {
    if (text ~ /^[[:space:]]*#/) next
    start = FNR
  }
  if (text ~ /\\$/) {
    sub(/\\$/, "", text)
    pending = pending text
    next
  }
  check(pending text, start)
  pending = ""
}
' "$@"
