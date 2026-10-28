#!/usr/bin/env bash
# Compare git-dirty with the original bash script, scripts/git-dirty.sh,
# repository by repository. Development aid only; not shipped.
#
# Usage: scripts/compare.sh [directory]
# GIT_DIRTY_OLD and GIT_DIRTY_NEW override the two programs.
#
# Both tools run with -a (stash and untracked checks). Each repository is
# reduced to "name<TAB>letters<TAB>branch", where letters are the sorted
# status letters plus ↑ for unpushed commits and branch is the name shown in
# parentheses (empty for main branches). The bash script's tree order is not
# used: its sort key places some entries under the wrong parent.
#
# A git-dirty worktree line (🌳) reduces to the worktree's ID, which git takes
# from its directory name, so it matches the bash script's line for the same
# directory. A worktree the bash script does not reach (outside the
# directory, or below an excluded one) is listed by git-dirty only: a + line.
set -euo pipefail

dir=${1:-$HOME/Code}
old=${GIT_DIRTY_OLD:-$(dirname "$0")/git-dirty.sh}
new=${GIT_DIRTY_NEW:-./git-dirty}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

GIT_DIRTY_COLOR=0 "$old" -a "$dir" | perl -CSD -ne '
  s/\r/\n/g;
  for (split /\n/) {
    next if /^Legend:/;
    next unless /(\x{2705}|\x{274C})/;
    s/^\s*(?:\x{2705}|\x{274C})\s//;
    my ($name, $st, $br) = /^(.*?)(?: \[([^\]]*)\])?(?: \(([^)]*)\))?$/;
    $st = join "", sort split //, ($st // "");
    print "$name\t$st\t", ($br // ""), "\n";
  }' | LC_ALL=C sort > "$tmp/old"

"$new" --plain -a "$dir" | perl -CSD -ne '
  next if /^Legend:/;
  next unless /(\x{2705}|\x{274C})/;
  chomp;
  s/^.*?(?:\x{2705}|\x{274C})\s//;
  s/ \x{B7} .*$//;
  my ($name, $st, $paren, $bare) = /^(.*?)(?: \[([^\]]*)\])?(?: \(([^)]*)\))?((?: [\x{2191}\x{2193}]\d+)*)$/;
  my $counts = ($paren // "") . ($bare // "");
  $st = $st // "";
  $st .= "\x{2191}" if $counts =~ /\x{2191}[1-9]/;
  $st = join "", sort split //, $st;
  my $br = $paren // "";
  $br =~ s/(?:,| [\x{2191}\x{2193}]).*$//;
  print "$name\t$st\t$br\n";' | LC_ALL=C sort > "$tmp/new"

old_n=$(wc -l < "$tmp/old")
new_n=$(wc -l < "$tmp/new")
echo "bash script: $old_n repositories; git-dirty: $new_n repositories"
if diff -u "$tmp/old" "$tmp/new" > "$tmp/diff"; then
  echo "no mismatches"
else
  echo "mismatches (- bash script, + git-dirty):"
  grep -E '^[-+][^-+]' "$tmp/diff"
  exit 1
fi
